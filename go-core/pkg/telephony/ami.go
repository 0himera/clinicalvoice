// Package telephony contains the control-plane adapter. Audio travels through
// the Python AudioSocket bridge, never through AMI.
package telephony

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var endpointRE = regexp.MustCompile(`^PJSIP/[A-Za-z0-9_+.@-]{1,100}$`)

func ValidEndpoint(s string) bool { return endpointRE.MatchString(s) }

type AMI struct{ Address, Username, Secret string }

func (a AMI) Originate(ctx context.Context, endpoint string, visitID int) error {
	if !ValidEndpoint(endpoint) || visitID <= 0 {
		return errors.New("invalid originate parameters")
	}
	return a.request(ctx, fmt.Sprintf("Action: Originate\r\nChannel: %s\r\nContext: voice-outbound\r\nExten: s\r\nPriority: 1\r\nTimeout: 30000\r\nAsync: true\r\nVariable: VISIT_ID=%d\r\nCallerID: Polyclinic 7 <7000707>", endpoint, visitID))
}

func (a AMI) Redirect(ctx context.Context, channel, target string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9/_.-]{1,160}$`).MatchString(channel) {
		return errors.New("invalid channel")
	}
	switch target {
	case "101", "102", "104", "105":
	default:
		return errors.New("invalid transfer target")
	}
	return a.request(ctx, fmt.Sprintf("Action: Redirect\r\nChannel: %s\r\nContext: from-internal\r\nExten: %s\r\nPriority: 1", channel, target))
}

func (a AMI) request(ctx context.Context, fields string) error {
	if a.Secret == "" || strings.ContainsAny(a.Username+a.Secret, "\r\n") {
		return errors.New("AMI credentials are not configured")
	}
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", a.Address)
	if err != nil {
		return fmt.Errorf("connect AMI: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, 4096)
	if _, err = r.ReadString('\n'); err != nil {
		return err
	}
	action := func(fields string) error {
		id := uuid.NewString()
		if _, err := fmt.Fprintf(conn, "ActionID: %s\r\n%s\r\n\r\n", id, fields); err != nil {
			return err
		}
		for n := 0; n < 100; n++ {
			message := map[string]string{}
			size := 0
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return err
				}
				size += len(line)
				if size > 32768 {
					return errors.New("AMI response too large")
				}
				line = strings.TrimSpace(line)
				if line == "" {
					break
				}
				key, v, ok := strings.Cut(line, ":")
				if ok {
					message[key] = strings.TrimSpace(v)
				}
			}
			if message["ActionID"] != id {
				continue
			}
			if message["Response"] != "Success" {
				return errors.New("AMI rejected action")
			}
			return nil
		}
		return errors.New("AMI response limit exceeded")
	}
	if err = action("Action: Login\r\nUsername: " + a.Username + "\r\nSecret: " + a.Secret + "\r\nEvents: off"); err != nil {
		return err
	}
	return action(fields)
}
