package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/mattn/go-isatty"
	"github.com/miekg/dns"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
)

var dnsServer string
var listenAddr string

var currentPhysicalZoneId = ""

func init() {
	flag.StringVar(&dnsServer, "dns-server", "169.254.169.253:53", "DNS resolver to use")
	flag.StringVar(&listenAddr, "listen-addr", "127.0.0.1:3333", "DNS server listen address")
	flag.Parse()
}

func main() {
	flag.Parse()

	l := getLogger()
	log := zapr.NewLogger(l)
	defer func() { _ = l.Sync() }()

	c, ctx, cancel := setupSignalHandling()
	defer func() {
		signal.Stop(c)
		cancel()
	}()

	log.Info("starting", "addr", listenAddr)

	em := ec2metadata.New(session.Must(session.NewSession()))
	zoneid, err := em.GetMetadataWithContext(ctx, "placement/availability-zone-id")
	if err != nil {
		log.Error(err, "cannot find physical zone id, will disable zone aware routing")
	}
	currentPhysicalZoneId = strings.ToLower(zoneid)

	log.Info("running in physical zone", "zone-id", currentPhysicalZoneId)

	var wg sync.WaitGroup

	h := &handler{
		ctx:       ctx,
		log:       log,
		dnsClient: &dns.Client{
			//TODO: configure timeouts etc
		},
	}

	// TCP
	tcpSrv := &dns.Server{
		Addr:    listenAddr,
		Net:     "tcp",
		Handler: h,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := tcpSrv.ListenAndServe(); err != nil {
			log.Error(err, "Failed to set listener")
			os.Exit(1)
		}
	}()

	// UDP
	udpSrv := &dns.Server{
		Addr:    listenAddr,
		Net:     "udp",
		Handler: h,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := udpSrv.ListenAndServe(); err != nil {
			log.Error(err, "Failed to set listener")
			os.Exit(1)
		}
	}()

	<-ctx.Done()

	log.Info("context done, shutting down the server")
	err = tcpSrv.Shutdown()
	if err != nil {
		log.Error(err, "cannot shutdown tcp server")
	}
	err = udpSrv.Shutdown()
	if err != nil {
		log.Error(err, "cannot shutdown udp server")
	}
	log.Info("shutdown, waiting for the worker to exit")
	wg.Wait()

	log.Info("exiting")
}

type handler struct {
	ctx       context.Context
	log       logr.Logger
	dnsClient *dns.Client
}

func getLogger() *zap.Logger {
	config := zap.NewDevelopmentConfig()
	config.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	if isatty.IsTerminal(os.Stdout.Fd()) {
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}
	l, err := config.Build()
	if err != nil {
		panic(fmt.Sprintf("log initialization failed: %v", err))
	}

	return l
}

func setupSignalHandling() (chan os.Signal, context.Context, context.CancelFunc) {
	var cancel context.CancelFunc
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-c:
			cancel()
		case <-ctx.Done():
		}
	}()

	return c, ctx, cancel
}
