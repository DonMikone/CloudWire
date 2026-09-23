#ifndef CLOUDWIRE_PLATFORM_H
#define CLOUDWIRE_PLATFORM_H

int cwOnBattery(void);
int cwLowPowerMode(void);
int cwMoveToTrash(const char *path, char **err);
void cwStartNetworkMonitor(void);
int cwStartPowerNotifications(void);

#endif
