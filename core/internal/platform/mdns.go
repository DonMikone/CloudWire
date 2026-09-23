//go:build darwin

package platform

/*
#include <dns_sd.h>
#include <stdlib.h>
#include <sys/select.h>
#include <arpa/inet.h>

static void cwRegisterReply(DNSServiceRef sdRef, DNSRecordRef rec, DNSServiceFlags flags,
                            DNSServiceErrorType err, void *ctx) {
	*(DNSServiceErrorType *)ctx = err;
}

// cwRegisterLoopbackHost publishes name as a local-only A record for
// 127.0.0.1 and waits up to timeoutSec for mDNSResponder's confirmation.
// On success *out holds the connection that keeps the record alive.
static DNSServiceErrorType cwRegisterLoopbackHost(const char *name, int timeoutSec, DNSServiceRef *out) {
	DNSServiceRef conn;
	DNSServiceErrorType err = DNSServiceCreateConnection(&conn);
	if (err) return err;
	struct in_addr addr;
	inet_pton(AF_INET, "127.0.0.1", &addr);
	DNSRecordRef rec;
	DNSServiceErrorType reply = kDNSServiceErr_Timeout;
	err = DNSServiceRegisterRecord(conn, &rec, kDNSServiceFlagsShared, kDNSServiceInterfaceIndexLocalOnly,
		name, kDNSServiceType_A, kDNSServiceClass_IN, sizeof(addr), &addr, 0, cwRegisterReply, &reply);
	if (!err) {
		int fd = DNSServiceRefSockFD(conn);
		fd_set set;
		FD_ZERO(&set);
		FD_SET(fd, &set);
		struct timeval tv = {timeoutSec, 0};
		if (select(fd + 1, &set, NULL, NULL, &tv) > 0) {
			err = DNSServiceProcessResult(conn);
		}
		if (!err) err = reply;
	}
	if (err) {
		DNSServiceRefDeallocate(conn);
		return err;
	}
	*out = conn;
	return 0;
}
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// RegisterLoopbackHost makes name (which must end in ".local") resolve to
// 127.0.0.1 on this Mac only; the record is never announced on the network.
// It stays registered until release is called or the process exits.
func RegisterLoopbackHost(name string) (release func(), err error) {
	if !strings.HasSuffix(strings.ToLower(name), ".local") {
		return nil, fmt.Errorf("register %q: not a .local name", name)
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var conn C.DNSServiceRef
	if rc := C.cwRegisterLoopbackHost(cname, 5, &conn); rc != 0 {
		return nil, fmt.Errorf("register %q with mDNSResponder: error %d", name, int(rc))
	}
	return func() { C.DNSServiceRefDeallocate(conn) }, nil
}
