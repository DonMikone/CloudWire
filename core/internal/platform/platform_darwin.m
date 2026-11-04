// Objective-C / IOKit / Network.framework probes for package platform.

#import <Foundation/Foundation.h>
#import <IOKit/ps/IOPowerSources.h>
#import <IOKit/ps/IOPSKeys.h>
#import <IOKit/pwr_mgt/IOPMLib.h>
#import <IOKit/IOMessage.h>
#import <Network/Network.h>
#include <pthread.h>
#include <string.h>
#include <stdlib.h>
#include "platform.h"
#include "_cgo_export.h"

int cwOnBattery(void) {
	CFTypeRef info = IOPSCopyPowerSourcesInfo();
	if (info == NULL) return 0;
	CFStringRef type = IOPSGetProvidingPowerSourceType(info);
	int onBattery = type != NULL && CFStringCompare(type, CFSTR(kIOPMBatteryPowerKey), 0) == kCFCompareEqualTo;
	CFRelease(info);
	return onBattery;
}

int cwLowPowerMode(void) {
	@autoreleasepool {
		return [[NSProcessInfo processInfo] isLowPowerModeEnabled] ? 1 : 0;
	}
}

int cwMoveToTrash(const char *path, char **err) {
	@autoreleasepool {
		NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
		NSError *e = nil;
		if ([[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:nil error:&e]) {
			return 0;
		}
		if (err != NULL) {
			*err = strdup([[e localizedDescription] UTF8String] ?: "unknown error");
		}
		return 1;
	}
}

// cwBundleIdentifier returns a malloc'ed copy of the bundle's identifier or NULL.
char *cwBundleIdentifier(const char *path) {
	CFURLRef url = CFURLCreateFromFileSystemRepresentation(NULL, (const UInt8 *)path, (CFIndex)strlen(path), true);
	if (url == NULL) return NULL;
	CFBundleRef bundle = CFBundleCreate(NULL, url);
	CFRelease(url);
	if (bundle == NULL) return NULL;
	char *out = NULL;
	CFStringRef ident = CFBundleGetIdentifier(bundle);
	if (ident != NULL) {
		CFIndex n = CFStringGetMaximumSizeForEncoding(CFStringGetLength(ident), kCFStringEncodingUTF8) + 1;
		out = malloc((size_t)n);
		if (out != NULL && !CFStringGetCString(ident, out, n, kCFStringEncodingUTF8)) {
			free(out);
			out = NULL;
		}
	}
	CFRelease(bundle);
	return out;
}

static nw_path_monitor_t cwMonitor;

void cwStartNetworkMonitor(void) {
	dispatch_queue_t q = dispatch_queue_create("io.github.donmikone.cloudwire.network", DISPATCH_QUEUE_SERIAL);
	cwMonitor = nw_path_monitor_create();
	nw_path_monitor_set_queue(cwMonitor, q);
	nw_path_monitor_set_update_handler(cwMonitor, ^(nw_path_t path) {
		int satisfied = nw_path_get_status(path) == nw_path_status_satisfied;
		goNetworkChanged(satisfied, nw_path_is_expensive(path) ? 1 : 0, nw_path_is_constrained(path) ? 1 : 0);
	});
	nw_path_monitor_start(cwMonitor);
}

static io_connect_t cwRootPort;
static IONotificationPortRef cwNotifyPort;
static io_object_t cwNotifier;

static void cwPowerCallback(void *refcon, io_service_t service, natural_t messageType, void *messageArgument) {
	switch (messageType) {
	case kIOMessageCanSystemSleep:
		IOAllowPowerChange(cwRootPort, (long)messageArgument);
		break;
	case kIOMessageSystemWillSleep:
		// Held back until running syncs stopped cleanly (platform.MaxSleepDelay).
		goSystemWillSleep();
		IOAllowPowerChange(cwRootPort, (long)messageArgument);
		break;
	case kIOMessageSystemHasPoweredOn:
		goSystemDidWake();
		break;
	default:
		break;
	}
}

static void *cwPowerThread(void *arg) {
	CFRunLoopAddSource(CFRunLoopGetCurrent(), IONotificationPortGetRunLoopSource(cwNotifyPort), kCFRunLoopCommonModes);
	CFRunLoopRun();
	return NULL;
}

int cwStartPowerNotifications(void) {
	cwRootPort = IORegisterForSystemPower(NULL, &cwNotifyPort, cwPowerCallback, &cwNotifier);
	if (cwRootPort == MACH_PORT_NULL) return 1;
	pthread_t t;
	if (pthread_create(&t, NULL, cwPowerThread, NULL) != 0) return 1;
	pthread_detach(t);
	return 0;
}
