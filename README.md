Example DNS resolver that does availability zone aware DNS lookup (in AWS EC2).

How it works:

1. On startup, it finds the current physical zone of the machine this is running at (by querying `http://169.254.169.254/latest/meta-data/placement/availability-zone-id`)
2. Starts a DNS resolver at given address
3. On receiving DNS request
    1. Assume we got the request for *foo.com* and our current zone id is *use1-az4*
    2. It will do a lookup (using given resolver or by default AWS VPC default resolver) for *use1-az4.foo.com*, if success, returns the result
    3. If step 2 failed, it will do a lookup for *foo.com* and return the result

To test:

1. Run the server

```
[surki@ip-10-246-11-26 tmp]$ go run .
2023-01-06T13:17:28.528Z        INFO    dns-zone-aware/dns.go:48        starting        {"addr": "127.0.0.1:3333"}
2023-01-06T13:17:28.534Z        INFO    dns-zone-aware/dns.go:57        running in physical zone        {"zone-id": "use1-az1"}
2023-01-06T13:21:33.418Z        INFO    dns-zone-aware/resolve.go:11    received dns request    {"req": ";; opcode: QUERY, status: NOERROR, id: 22687\n;; flags: rd ad; QUERY: 1, ANSWER: 0, AUTHORITY: 0, ADDITIONAL: 1\n\n;; OPT PSEUDOSECTION:\n; EDNS: version 0; flags: ; udp: 4096\n\n;; QUESTION SECTION:\n;foo.com.\tIN\t A\n"}
2023-01-06T13:21:33.418Z        INFO    dns-zone-aware/resolve.go:16    handling A question     {"domain": "foo.com."}
2023-01-06T13:21:33.418Z        INFO    dns-zone-aware/resolve.go:41    forwarding request      {"resolver": "169.254.169.253:53"}
2023-01-06T13:21:33.418Z        INFO    dns-zone-aware/resolve.go:53    doing dns lookup        {"req": ";; opcode: QUERY, status: NOERROR, id: 29782\n;; flags: rd; QUERY: 1, ANSWER: 0, AUTHORITY: 0, ADDITIONAL: 1\n\n;; OPT PSEUDOSECTION:\n; EDNS: version 0; flags: do; udp: 4096\n\n;; QUESTION SECTION:\n;use1-az1.foo.com.\tIN\t A\n"}
2023-01-06T13:21:33.418Z        INFO    dns-zone-aware/resolve.go:73    dns lookup finished     {"ans-rcode": 0, "resp-time": "185.061µs"}
2023-01-06T13:21:33.418Z        INFO    dns-zone-aware/resolve.go:35    sending response        {"response": ";; opcode: QUERY, status: NOERROR, id: 22687\n;; flags: qr rd ra; QUERY: 1, ANSWER: 1, AUTHORITY: 0, ADDITIONAL: 1\n\n;; OPT PSEUDOSECTION:\n; EDNS: version 0; flags: do; udp: 4096\n\n;; QUESTION SECTION:\n;foo.com.\tIN\t A\n\n;; ANSWER SECTION:\nuse1-az1.foo.com.\t293\tIN\tA\t34.206.39.153\n"}

```

2. From other terminal

```
[surki@ip-10-246-11-26 ~]$ dig -p 3333 foo.com @127.0.0.1

; <<>> DiG 9.11.4-P2-RedHat-9.11.4-26.P2.amzn2.5.2 <<>> -p 3333 foo.com @127.0.0.1
;; global options: +cmd
;; Got answer:
;; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 22687
;; flags: qr rd ra; QUERY: 1, ANSWER: 1, AUTHORITY: 0, ADDITIONAL: 1

;; OPT PSEUDOSECTION:
; EDNS: version: 0, flags: do; udp: 4096
;; QUESTION SECTION:
;foo.com.                       IN      A

;; ANSWER SECTION:
foo.com.       293     IN      A       34.206.39.153

;; Query time: 0 msec
;; SERVER: 127.0.0.1#3333(127.0.0.1)
;; WHEN: Fri Jan 06 13:21:33 UTC 2023
;; MSG SIZE  rcvd: 68
```
