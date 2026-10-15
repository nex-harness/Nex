// neterr.go: the DNS error alias used by the transient-error classifier.
package modelprovider

import "net"

type netDNSError = net.DNSError
