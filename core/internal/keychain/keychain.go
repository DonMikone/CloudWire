//go:build darwin

// Package keychain stores generic passwords in the macOS login Keychain.
package keychain

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

static CFStringRef cwStr(const char *s) {
	return CFStringCreateWithCString(kCFAllocatorDefault, s, kCFStringEncodingUTF8);
}

static CFMutableDictionaryRef cwQuery(const char *service, const char *account) {
	CFMutableDictionaryRef q = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFStringRef svc = cwStr(service), acc = cwStr(account);
	CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(q, kSecAttrService, svc);
	CFDictionarySetValue(q, kSecAttrAccount, acc);
	CFRelease(svc);
	CFRelease(acc);
	return q;
}

// cwGet copies the secret into a malloc'd buffer the caller frees.
static OSStatus cwGet(const char *service, const char *account, char **out, long *outLen) {
	CFMutableDictionaryRef q = cwQuery(service, account);
	CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus st = SecItemCopyMatching(q, &result);
	CFRelease(q);
	if (st != errSecSuccess) return st;
	CFDataRef data = (CFDataRef)result;
	long n = CFDataGetLength(data);
	*out = malloc(n > 0 ? n : 1);
	memcpy(*out, CFDataGetBytePtr(data), n);
	*outLen = n;
	CFRelease(result);
	return errSecSuccess;
}

static OSStatus cwSet(const char *service, const char *account, const char *secret, long n) {
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)secret, n);
	CFMutableDictionaryRef q = cwQuery(service, account);
	CFMutableDictionaryRef upd = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(upd, kSecValueData, data);
	OSStatus st = SecItemUpdate(q, upd);
	if (st == errSecItemNotFound) {
		CFDictionarySetValue(q, kSecValueData, data);
		CFDictionarySetValue(q, kSecAttrAccessible, kSecAttrAccessibleAfterFirstUnlock);
		CFStringRef label = cwStr("CloudWire");
		CFDictionarySetValue(q, kSecAttrLabel, label);
		CFRelease(label);
		st = SecItemAdd(q, NULL);
	}
	CFRelease(upd);
	CFRelease(q);
	CFRelease(data);
	return st;
}

static OSStatus cwDelete(const char *service, const char *account) {
	CFMutableDictionaryRef q = cwQuery(service, account);
	OSStatus st = SecItemDelete(q);
	CFRelease(q);
	return st;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// ErrNotFound is returned when no item exists.
var ErrNotFound = errors.New("keychain item not found")

func statusErr(op string, st C.OSStatus) error {
	if st == C.errSecItemNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("keychain %s: OSStatus %d", op, int(st))
}

// Get reads a generic password.
func Get(service, account string) (string, error) {
	cs, ca := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	var out *C.char
	var n C.long
	if st := C.cwGet(cs, ca, &out, &n); st != C.errSecSuccess {
		return "", statusErr("get", st)
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoStringN(out, C.int(n)), nil
}

// Set creates or replaces a generic password.
func Set(service, account, secret string) error {
	cs, ca, sec := C.CString(service), C.CString(account), C.CString(secret)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	defer C.free(unsafe.Pointer(sec))
	if st := C.cwSet(cs, ca, sec, C.long(len(secret))); st != C.errSecSuccess {
		return statusErr("set", st)
	}
	return nil
}

// Delete removes a generic password. Missing items are not an error.
func Delete(service, account string) error {
	cs, ca := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	if st := C.cwDelete(cs, ca); st != C.errSecSuccess && st != C.errSecItemNotFound {
		return statusErr("delete", st)
	}
	return nil
}
