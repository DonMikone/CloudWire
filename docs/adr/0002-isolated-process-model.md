# Isolated worker processes

A small supervisor spawns one worker process per active Mount and short-lived sync/migration workers, instead of running everything in one process. A hung or crashing Mount cannot stall the others. macOS background priority (`PRIO_DARWIN_BG`) is per process, so sync must be a separate process anyway. The cost is one extra Go runtime per active Mount.
