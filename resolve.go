package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func (h *handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	h.log.Info("received dns request", "req", r)

	// TODO: handle multiple questions?
	// Not handling it right now as most of the DNS resolver handles only first question and typeA is our only use case now.
	// Even with multiple DNS questions, the resulting MsgHdr has only global Rcode not for each Question.
	switch r.Question[0].Qtype {
	case dns.TypeA:
		h.log.Info("handling A question", "domain", r.Question[0].Name)

		names := h.dnsNamesToUse(r.Question[0].Name)
		res, err := h.resolveDnsNames(names)
		if err != nil {
			h.log.Info("no resolv error, sending error")
			if res != nil {
				res.SetReply(r)
				w.WriteMsg(res)
			} else {
				m := new(dns.Msg)
				m.SetReply(r)
				m.SetRcode(r, dns.RcodeServerFailure)
				w.WriteMsg(m)
			}
			return
		}

		if a, ok := res.Answer[0].(*dns.A); ok {
			a.Hdr.Name = r.Question[0].Name
		}
		res.SetReply(r)
		h.log.Info("sending response", "response", res)
		w.WriteMsg(res)
	}
}

func (h *handler) resolveDnsNames(names []string) (*dns.Msg, error) {

	dnsClient := h.dnsClient
	dnsServer := h.dnsServer

	for _, name := range names {
		h.log.Info("forwarding request", "resolver", dnsServer)
		attempt := 1
		resolveDnsServerOnce := false

	Redo:
		dnsRequest := new(dns.Msg)
		dnsRequest.SetQuestion(name, dns.TypeA)
		dnsRequest.SetEdns0(4096, true)

		h.log.Info("doing dns lookup", "req", dnsRequest)
		ans, rtt, err := dnsClient.Exchange(dnsRequest, dnsServer)
		if err != nil {
			// if it is a dial error, retry after resolving dns server again
			if _, ok := err.(*net.OpError); ok && !resolveDnsServerOnce {
				h.log.Info(fmt.Sprintf("Received Dial Error: %v. Resolved DNS Server Again and Retrying dns lookup. Attempt: %v", err, attempt))
				dnsServer = resolveDnsServerAddress(h.log)
				resolveDnsServerOnce = true
				goto Redo
			}
			if inputConfig.MaxRetries >= attempt {
				backOffTime := h.backoff.Next(attempt)
				time.Sleep(backOffTime)
				h.log.Info(fmt.Sprintf("Received Error: %v. Retrying dns lookup. Attempt: %v after %v", err, attempt, backOffTime))
				// if it is a dial error, reset dnsServer to inputConfig.dnsServer
				if _, ok := err.(*net.OpError); ok {
					dnsServer = inputConfig.dnsServer
				}
				attempt++
				goto Redo
			}
			h.log.Error(err, "cannot resolve dns", "dns-request", dnsRequest)
			return nil, err
		}
		if ans.MsgHdr.Truncated {
			if inputConfig.MaxRetries >= attempt {
				dnsClient = new(dns.Client)
				dnsClient.Net = "tcp"
				h.log.Info(fmt.Sprintf("Received Truncated response. Retrying dns lookup. Attempt: %v", attempt))
				attempt++
				goto Redo
			}
		}

		if len(ans.Answer) == 0 {
			h.log.Info("lookup failed with no answers, ignoring this domain", "domain", name)
			continue
		}

		h.log.Info("dns lookup finished", "ans-rcode", ans.MsgHdr.Rcode, "resp-time", rtt)

		switch ans.MsgHdr.Rcode {
		case dns.RcodeNameError:
			h.log.Info("lookup failed with nxdomain, ignoring this domain", "domain", name)
			continue
		case dns.RcodeServerFailure:
			return ans, fmt.Errorf("Name server encountered an internal failure while processing this request (SERVFAIL)")
		case dns.RcodeRefused:
			return ans, fmt.Errorf("Name server refused to process the request (REFUSED)")
		case dns.RcodeSuccess:
			return ans, nil
		default:
			return ans, fmt.Errorf("Name server returned error, rcode=%v", ans.MsgHdr.Rcode)
		}
	}

	return nil, fmt.Errorf("cannot resolve dns name(s): %v", names)
}

func (h *handler) dnsNamesToUse(s string) []string {
	// currentPhysicalZoneId = "use1-az1"
	if currentPhysicalZoneId == "" {
		return []string{s}
	}

	if !strings.HasSuffix(s, ".") {
		s = s + "."
	}

	return []string{currentPhysicalZoneId + inputConfig.prefixSeparator + s, s}
}
