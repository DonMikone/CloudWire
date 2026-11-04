package operations

import (
	"context"

	"github.com/rclone/rclone/fs"
)

// CloudWire patch (see docs/adr/0012, docs/adr/0014): bisync records every
// completed copy in a per-file journal as soon as it lands, so an aborted or
// killed run cannot lose the record of what CloudWire itself wrote. Sync's
// logger only reports intents and errors, never a finished transfer.

// TransferDoneFn is called after src was copied to dst successfully. dst is
// the object as the destination reports it after the transfer.
type TransferDoneFn func(ctx context.Context, src, dst fs.Object)

type transferDoneKey struct{}

// WithTransferDoneFn returns a copy of ctx in which fn is called for every
// completed copy of a sync.
func WithTransferDoneFn(ctx context.Context, fn TransferDoneFn) context.Context {
	return context.WithValue(ctx, transferDoneKey{}, fn)
}

// ReportTransferDone calls the TransferDoneFn stored in ctx, if any.
func ReportTransferDone(ctx context.Context, src, dst fs.Object) {
	if fn, ok := ctx.Value(transferDoneKey{}).(TransferDoneFn); ok && src != nil && dst != nil {
		fn(ctx, src, dst)
	}
}
