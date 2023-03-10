package main

import (
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

func (h *handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	h.log.Info("received dns request", "req", r)

	// TODO: handle multiple questions?
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
	h.log.Info("forwarding request", "resolver", dnsServer)

	dnsClient := h.dnsClient

	for _, name := range names {
		retried := false

	Redo:
		dnsRequest := new(dns.Msg)
		dnsRequest.SetQuestion(name, dns.TypeA)
		dnsRequest.SetEdns0(4096, true)

		h.log.Info("doing dns lookup", "req", dnsRequest)
		ans, rtt, err := dnsClient.Exchange(dnsRequest, dnsServer)
		if err != nil {
			if !retried {
				// Add backoff
				retried = true
				goto Redo
			}
			h.log.Error(err, "cannot resolve dns", "dns-request", dnsRequest)
			return nil, err
		}
		if ans.MsgHdr.Truncated {
			if !retried {
				retried = true
				dnsClient = new(dns.Client)
				dnsClient.Net = "tcp"
				goto Redo
			}
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

	return []string{currentPhysicalZoneId + "." + s, s}
}
