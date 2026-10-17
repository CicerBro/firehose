package main

import (
	"context"
	"net/url"

	"github.com/CicerBro/speedtest/internal/client"
	"github.com/CicerBro/speedtest/internal/rtc"
)

// rtcLoss plugs the pion WebRTC loss test (internal/rtc) into the CLI client's loss seam.
func rtcLoss(ctx context.Context, p client.LossParams) (client.Loss, error) {
	network := ""
	switch p.IPFamily {
	case 4:
		network = "udp4"
	case 6:
		network = "udp6"
	}
	r, err := rtc.RunClient(ctx, rtc.ClientOptions{
		OfferURL:   p.Server + "/api/rtc/offer?sid=" + url.QueryEscape(p.SID),
		HTTPClient: p.HTTPClient,
		Header:     p.Header,
		PPS:        p.PPS,
		DurationMs: int(p.Duration.Milliseconds()),
		Size:       p.Size,
		Network:    network,
	})
	return client.Loss(r), err
}
