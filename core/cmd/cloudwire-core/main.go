// Command cloudwire-core is the CloudWire Core: supervisor, worker and a
// small diagnostic RPC client.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/rclone/rclone/lib/atexit"

	"github.com/DonMikone/CloudWire/core/internal/buildinfo"
	"github.com/DonMikone/CloudWire/core/internal/daemon"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/worker"
)

const usage = `usage:
  cloudwire-core serve [--force]     run the Core supervisor
  cloudwire-core worker              run one job (reads it from stdin)
  cloudwire-core rpc <method> [json] call the running Core and print the result
  cloudwire-core events              print Core events as JSON lines
  cloudwire-core version             print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		force := len(os.Args) > 2 && os.Args[2] == "--force"
		// The supervisor handles SIGINT/SIGTERM itself (orderly shutdown).
		atexit.IgnoreSignals()
		os.Exit(daemon.Serve(daemon.Options{Force: force}))
	case "worker":
		os.Exit(worker.Run(os.Stdin, os.Stdout, os.Stderr, paths.Default()))
	case "rpc":
		os.Exit(rpc(os.Args[2:]))
	case "events":
		os.Exit(events())
	case "version", "--version", "-v":
		fmt.Println("cloudwire-core", buildinfo.Version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func rpc(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	params := json.RawMessage("{}")
	if len(args) > 1 {
		if !json.Valid([]byte(args[1])) {
			fmt.Fprintln(os.Stderr, "params must be valid JSON")
			return 2
		}
		params = json.RawMessage(args[1])
	}
	conn, err := net.DialTimeout("unix", paths.Default().Socket, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the Core:", err)
		return 1
	}
	defer conn.Close()
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": args[0], "params": params})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	r := bufio.NewReaderSize(conn, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "no response:", err)
			return 1
		}
		var resp struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID == nil {
			continue // an event
		}
		out := resp.Result
		code := 0
		if len(resp.Error) > 0 {
			out, code = resp.Error, 1
		}
		var pretty any
		_ = json.Unmarshal(out, &pretty)
		b, _ := json.MarshalIndent(pretty, "", "  ")
		if code != 0 {
			fmt.Fprintln(os.Stderr, string(b))
		} else {
			fmt.Println(string(b))
		}
		return code
	}
}

// events subscribes and prints every event's params as one JSON line.
func events() int {
	conn, err := net.DialTimeout("unix", paths.Default().Socket, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the Core:", err)
		return 1
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"events.subscribe","params":{"client":"cli"}}` + "\n")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	r := bufio.NewReaderSize(conn, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.Method == "event" {
			fmt.Println(string(msg.Params))
		}
	}
}
