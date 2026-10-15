// Command cli-test is an HTTP dialogue client; it uses the same application as SIP.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/himera/tochka-voice/go-core/pkg/config"
	"github.com/himera/tochka-voice/go-core/pkg/httpapi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	base := os.Getenv("CORE_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	client := &http.Client{Timeout: 40 * time.Second}
	post := func(path string, data any) (httpapi.Response, error) {
		b, err := json.Marshal(data)
		if err != nil {
			return httpapi.Response{}, err
		}
		req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(b))
		if err != nil {
			return httpapi.Response{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if token := os.Getenv("CORE_API_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return httpapi.Response{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return httpapi.Response{}, fmt.Errorf("core HTTP %d", resp.StatusCode)
		}
		var result httpapi.Response
		err = json.NewDecoder(resp.Body).Decode(&result)
		return result, err
	}
	response, err := post("/api/dialogue/start", map[string]string{})
	if err != nil {
		return err
	}
	id := response.CallID
	defer func() { _, _ = post("/api/dialogue/"+id+"/end", nil) }()
	fmt.Println(response.BotSpeech)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "exit" {
			return nil
		}
		response, err = post("/api/dialogue/turn", map[string]string{"call_id": id, "turn_id": uuid.NewString(), "text": text})
		if err != nil {
			return err
		}
		fmt.Println(response.BotSpeech)
		if response.ShouldHangup || response.TransferTarget != "" {
			return nil
		}
	}
}
