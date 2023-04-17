package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/mattn/go-isatty"
	"github.com/miekg/dns"
	"github.com/surki/dns-zone-aware/internal"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
)

type Config struct {
	dnsServer             string
	listenAddr            string
	prefixSeparator       string
	useKubeDnsServer      bool
	dnsServerTimeout      int64
	BackOffStrategy       string
	BackOffMaxJitter      int64
	BackOffInterval       int64
	BackOffMaxTimeout     float64
	BackOffInitialTimeout float64
	BackOffExponentFactor float64
	MaxRetries            int
}

var inputConfig Config

var currentPhysicalZoneId = ""

func init() {
	inputConfig = Config{}
	flag.StringVar(&inputConfig.dnsServer, "dns-server", "169.254.169.253:53", "DNS resolver to use")
	flag.StringVar(&inputConfig.listenAddr, "listen-addr", "127.0.0.1:53", "DNS server listen address")
	inputConfig.dnsServerTimeout = *flag.Int64("dns-server.timeoutMillis", 5000, "Timeout for DNS server")
	flag.StringVar(&inputConfig.BackOffStrategy, "dns-server.backoff-strategy", "exponential", "Backoff Strategy to use when request to DNS Server are retried. exponential or constant")
	inputConfig.BackOffMaxJitter = *flag.Int64("dns-server.backoff-maxjitter", 10, "Jitter for BackOff computation")
	inputConfig.BackOffInterval = *flag.Int64("dns-server.backoff-interval", 100, "Interval for Constant BackOff computation")
	inputConfig.BackOffMaxTimeout = *flag.Float64("dns-server.backoff-maxtimeout", 1000, "Max Timeout for Exponential BackOff computation")
	inputConfig.BackOffInitialTimeout = *flag.Float64("dns-server.backoff-initialtimeout", 100, "Initial Timeout for Exponential BackOff computation")
	inputConfig.BackOffExponentFactor = *flag.Float64("dns-server.backoff-expfactor", 2, "Factor for Exponential BackOff computation")
	inputConfig.MaxRetries = *flag.Int("dns-server.retries", 3, "No of Retries for DNS server")
	flag.StringVar(&inputConfig.prefixSeparator, "dns-server.prefix-separator", ".", "Separator to use when prefixing the zoneid to DNS")
	flag.BoolVar(&inputConfig.useKubeDnsServer, "dns-server.use-kube-dns", false, "Use the KubeDNS server to resolve the DNS queries")
	flag.Parse()
}

func main() {

	l := getLogger()
	log := zapr.NewLogger(l)
	defer func() { _ = l.Sync() }()

	c, ctx, cancel := setupSignalHandling()
	defer func() {
		signal.Stop(c)
		cancel()
	}()

	log.Info("starting", "addr", inputConfig.listenAddr)

	em := ec2metadata.New(session.Must(session.NewSession()))
	zoneid, err := em.GetMetadataWithContext(ctx, "placement/availability-zone-id")
	if err != nil {
		log.Error(err, "cannot find physical zone id, will disable zone aware routing")
	}
	currentPhysicalZoneId = strings.ToLower(zoneid)

	log.Info("running in physical zone", "zone-id", currentPhysicalZoneId)

	var wg sync.WaitGroup

	h := &handler{
		ctx: ctx,
		log: log,
		dnsClient: &dns.Client{
			Timeout: time.Duration(time.Duration(inputConfig.dnsServerTimeout).Milliseconds()),
		},
		backoff:   initBackOffStrategy(),
		dnsServer: resolveDnsServerAddress(log),
	}

	// TCP
	tcpSrv := &dns.Server{
		Addr:    inputConfig.listenAddr,
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
		Addr:    inputConfig.listenAddr,
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
	dnsServer string
	backoff   internal.Backoff
}

func initBackOffStrategy() internal.Backoff {
	switch inputConfig.BackOffStrategy {
	case "exponential":
		return internal.NewExponentialBackoff(time.Duration(inputConfig.BackOffInitialTimeout*float64(time.Millisecond)),
			time.Duration(inputConfig.BackOffMaxTimeout*float64(time.Millisecond)),
			inputConfig.BackOffExponentFactor,
			time.Duration(inputConfig.BackOffMaxJitter*int64(time.Millisecond)))
	case "constant":
		return internal.NewConstantBackoff(time.Duration(inputConfig.BackOffInterval*int64(time.Millisecond)),
			time.Duration(inputConfig.BackOffMaxJitter*int64(time.Millisecond)))
	default:
		return internal.NewConstantBackoff(time.Duration(inputConfig.BackOffInterval*int64(time.Millisecond)),
			time.Duration(inputConfig.BackOffMaxJitter*int64(time.Millisecond)))
	}
}

func resolveDnsServerAddress(log logr.Logger) string {
	if inputConfig.useKubeDnsServer {
		ip, err := findKubeDnsServerIp(log)
		if err == nil {
			return ip
		}
	}
	log.Info("Falling back to dnsServer from config", "dnsServer", inputConfig.dnsServer)
	return inputConfig.dnsServer
}

// Kubernetes Client in cluster call to fetch the DNS service ip and return err
func findKubeDnsServerIp(log logr.Logger) (string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Error(err, "cannot create kubernetes client config")
		return "", err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Error(err, "cannot create kubernetes client")
		return "", err
	}
	svc, err := clientset.CoreV1().Services("kube-system").Get(context.Background(), "kube-dns", v1.GetOptions{})
	if err != nil {
		log.Error(err, "cannot find kube-dns service")
		return "", err
	}
	if svc.Spec.ClusterIP == "" {
		log.Error(err, "kube-dns service does not have a cluster ip")
		return "", errors.New("kube-dns service does not have a cluster ip")
	}
	return svc.Spec.ClusterIP + ":53", nil

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
