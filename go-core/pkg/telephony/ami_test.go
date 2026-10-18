package telephony

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestOriginateAndRedirectAMIContract(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = fmt.Fprint(conn, "Asterisk Call Manager/11.0\r\n")
			reader := bufio.NewReader(conn)
			for j := 0; j < 2; j++ {
				fields := map[string]string{}
				var lines []string
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						_ = conn.Close()
						return
					}
					line = strings.TrimSpace(line)
					if line == "" {
						break
					}
					lines = append(lines, line)
					key, val, _ := strings.Cut(line, ":")
					fields[key] = strings.TrimSpace(val)
				}
				if j == 1 {
					commands <- strings.Join(lines, "\n")
				}
				_, _ = fmt.Fprintf(conn, "Response: Success\r\nActionID: %s\r\n\r\n", fields["ActionID"])
			}
			_ = conn.Close()
		}
	}()
	ami := AMI{Address: listener.Addr().String(), Username: "test", Secret: "test-secret"}
	if err = ami.Originate(context.Background(), "PJSIP/200", 5001); err != nil {
		t.Fatal(err)
	}
	if err = ami.Redirect(context.Background(), "PJSIP/200-00000001", "104"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Variable: VISIT_ID=5001", "Exten: 104"} {
		select {
		case command := <-commands:
			if !strings.Contains(command, want) {
				t.Fatalf("missing %q in %s", want, command)
			}
		case <-time.After(time.Second):
			t.Fatal("AMI command missing")
		}
	}
}
func TestRejectsControlProtocolInjection(t *testing.T) {
	for _, endpoint := range []string{"SIP/+7999", "PJSIP/200\r\nAction: Command", "PJSIP/../../x", "PJSIP/200;foo"} {
		if ValidEndpoint(endpoint) {
			t.Errorf("unsafe endpoint accepted: %q", endpoint)
		}
	}
	if err := (AMI{}).Redirect(context.Background(), "PJSIP/200", "103"); err == nil {
		t.Fatal("internal 103 accepted as emergency target")
	}
}

func TestAMIFieldsRejectInjectionAtEncodingBoundary(t *testing.T) {
	for _, fields := range []map[string]string{{"Channel": "PJSIP/200\r\nAction: Command"}, {"Action\r\nCommand": "x"}, {"ActionID": "injected"}} {
		if _, err := amiFields(fields); err == nil {
			t.Fatal("AMI encoder accepted injection")
		}
	}
}
