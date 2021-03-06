package main

import (
	pb "clusrun/protobuf"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/judwhite/go-svc/svc"
)

const (
	pprofServer = "0.0.0.0:8080"
)

var (
	localHost string
)

func main() {
	if len(os.Args) < 2 {
		displayNodeUsage()
		return
	}
	initGlobalVars()
	cmd, args := os.Args[1], os.Args[2:]
	switch strings.ToLower(cmd) {
	case "start":
		start(args)
	case "config":
		config(args)
	default:
		displayNodeUsage()
	}
}

func displayNodeUsage() {
	Printlnf(`
Usage: 
	clusnode <command> [options]

The commands are:
	start           - start the node
	config          - configure the started node

Usage of start:
	clusnode start [options]
	clusnode start -h

Usage of config:
	clusnode config <command> [configs]
	clusnode config -h

`)
}

func initGlobalVars() {
	RunOnWindows = runtime.GOOS == "windows"

	LineEnding = "\n"
	if RunOnWindows {
		LineEnding = "\r\n"
	}

	var err error
	if ExecutablePath, err = os.Executable(); err != nil {
		Fatallnf("Failed to get executable path: %v", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		Fatallnf("Failed to get hostname: %v", err)
	}
	NodeName = strings.ToUpper(hostname)

	localHost = NodeName + ":" + DefaultPort

	Tls.Enabled = true
	curDir := filepath.Dir(ExecutablePath)
	cert, key := filepath.Join(curDir, "cert.pem"), filepath.Join(curDir, "key.pem")
	if _, err := os.Stat(cert); os.IsNotExist(err) {
		Tls.Enabled = false
	} else {
		Tls.CertFile = cert
	}
	if _, err := os.Stat(key); os.IsNotExist(err) {
		Tls.Enabled = false
	} else {
		Tls.KeyFile = key
	}
}

func start(args []string) {
	fs := flag.NewFlagSet("clusnode start options", flag.ExitOnError)
	default_config_file := ExecutablePath + ".config"
	default_log_dir := ExecutablePath + ".logs"
	default_log_file_label := filepath.Join(default_log_dir, "<host>.<start time>.log")
	config_file := fs.String("config-file", default_config_file, "specify the config file for saving and loading settings")
	headnodes := fs.String("headnodes", "", "specify the host addresses of headnodes for this clusnode to join in")
	host := fs.String("host", localHost, "specify the host address of this headnode and clusnode")
	log_file := fs.String("log-file", default_log_file_label, "specify the file for logging")
	pprof := fs.Bool("pprof", false, fmt.Sprintf("start HTTP server on %v for pprof", pprofServer))
	_ = fs.Parse(args)

	// Setup the host address of this node
	var err error
	if _, _, NodeHost, err = ParseHostAddress(*host); err != nil {
		Fatallnf("Failed to parse node host address: %v", err)
	}

	// Setup log file
	if *log_file == default_log_file_label {
		if err := os.MkdirAll(default_log_dir, 0644); err != nil {
			Fatallnf("Failed to create log dir: %v", err)
		}
		file_name := fmt.Sprintf("%v.%v", FileNameFormatHost(NodeHost), time.Now().Format("20060102150405.log"))
		*log_file = filepath.Join(default_log_dir, file_name)
	}
	f, err := os.OpenFile(*log_file, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		Fatallnf("Failed to open log file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)
	Printlnf("Log file: %v", *log_file)

	// Catch and log panic
	defer LogPanicBeforeExit()

	// Start HTTP server for pprof
	if *pprof {
		LogInfo("Start pprof HTTP server on %v", pprofServer)
		go func() {
			if err := http.ListenAndServe(pprofServer, nil); err != nil {
				LogError("Failed to start pprof HTTP server")
			}
		}()
	}

	// Setup config file
	NodeConfigFile = *config_file
	LogInfo("Config file: %v", NodeConfigFile)
	LoadNodeConfigs()

	// Setup headnodes
	if *headnodes != "" {
		LogInfo("Adding headnodes: %v", *headnodes)
		for _, headnode := range strings.Split(*headnodes, ",") {
			if _, _, _, err := ParseHostAddress(headnode); err != nil {
				LogFatality("Failed to parse headnode host address: %v", err)
			} else {
				_, _ = AddHeadnode(headnode)
			}
		}
	}
	if connected, connecting := GetHeadnodes(); len(connected)+len(connecting) == 0 {
		LogInfo("Adding default headnode: %v", NodeHost)
		_, _ = AddHeadnode(NodeHost)
	}
	SaveNodeConfigs()

	// Start node service
	prg := &program{}
	if err := svc.Run(prg); err != nil {
		LogFatality("Failed to start service: %v", err)
	}
	LogInfo("Exited")
}

func config(args []string) {
	if len(args) == 0 {
		displayConfigUsage()
		return
	}

	command := strings.ToLower(args[0])
	fs := flag.NewFlagSet("clusnode config options", flag.ExitOnError)
	node := fs.String("node", localHost, "specify the node to config")
	var mode pb.SetHeadnodesMode
	switch strings.ToLower(command) {
	case "add":
		mode = pb.SetHeadnodesMode_Add
	case "set":
		mode = pb.SetHeadnodesMode_Default
	case "del":
		mode = pb.SetHeadnodesMode_Remove
	case "get":
		_ = fs.Parse(args[1:])
		setOrGetConfig(*node, false, nil, 0, nil, nil)
		return
	default:
		displayConfigUsage()
		return
	}

	headnodes := fs.String("headnodes", "", fmt.Sprintf("%s headnodes for this clusnode to join in", command))
	var store_output, timeout, max_job_count, interval *string
	if command == "set" {
		store_output = fs.String("store-output", "", "set if store job output on this headnode")
		timeout = fs.String("heartbeat-timeout", "", "set the heartbeat timeout of this headnode")
		max_job_count = fs.String("max-job-count", "", "set the count of jobs to keep in history on this headnode")
		interval = fs.String("heartbeat-interval", "", "set the heartbeat interval of this clusnode")
	}
	_ = fs.Parse(args[1:])
	if fs.NFlag() == 0 {
		fs.PrintDefaults()
		return
	}

	var nodes []string
	if *headnodes != "" {
		nodes = strings.Split(*headnodes, ",")
	}
	headnode_config := make(map[string]string)
	if store_output != nil && *store_output != "" {
		headnode_config[Config_Headnode_StoreOutput.Name] = *store_output
	}
	if timeout != nil && *timeout != "" {
		headnode_config[Config_Headnode_HeartbeatTimeoutSecond.Name] = *timeout
	}
	if max_job_count != nil && *max_job_count != "" {
		headnode_config[Config_Headnode_MaxJobCount.Name] = *max_job_count
	}
	clusnode_config := make(map[string]string)
	if interval != nil && *interval != "" {
		clusnode_config[Config_Clusnode_HeartbeatIntervalSecond.Name] = *interval
	}
	setOrGetConfig(*node, true, nodes, mode, headnode_config, clusnode_config)
}

func displayConfigUsage() {
	Printlnf(`
Usage: 
	clusnode config <command> [configs]

The commands are:
	add           - add headnodes for clusnode role
	del           - delete headnodes for clusnode role
	set           - set the configs for clusnode role and headnode role
	get           - get the configs for clusnode role and headnode role

`)
}

func setOrGetConfig(node string, set bool, headnodes []string, mode pb.SetHeadnodesMode, headnode_config, clusnode_config map[string]string) {
	// Parse target node host
	_, _, host, err := ParseHostAddress(node)
	if err != nil {
		Fatallnf("Failed to parse the host of node to config: %v", err)
	}

	// Setup connection
	conn, cancel := ConnectNode(host)
	defer cancel()
	if conn == nil {
		Fatallnf("Please ensure the node is started.")
	}
	defer conn.Close()

	// Define print function
	do := "Get"
	if set {
		do = "Set"
	}
	print_result := func(item string, result map[string]string, err error) {
		if err != nil {
			Printlnf("%v %v failed: %v", do, item, err)
		} else {
			Printlnf("%v %v result:", do, item)
			for k, v := range result {
				Printlnf("\t%q: %v", k, v)
			}
		}
	}

	label_clusnode_config := Config_Clusnode + " configs"
	label_headnode_config := Config_Headnode + " configs"
	if set {
		// Set headnodes
		if len(headnodes) > 0 {
			c := pb.NewClusnodeClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reply, err := c.SetHeadnodes(ctx, &pb.SetHeadnodesRequest{Headnodes: headnodes, Mode: mode})
			print_result("headnodes", reply.GetResults(), err)
		}

		// Set clusnode role configs
		if len(clusnode_config) > 0 {
			c := pb.NewClusnodeClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reply, err := c.SetConfigs(ctx, &pb.SetConfigsRequest{Configs: clusnode_config})
			print_result(label_clusnode_config, reply.GetResults(), err)
		}

		// Set headnode role configs
		if len(headnode_config) > 0 {
			c := pb.NewHeadnodeClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reply, err := c.SetConfigs(ctx, &pb.SetConfigsRequest{Configs: headnode_config})
			print_result(label_headnode_config, reply.GetResults(), err)
		}
	} else {
		// Get clusnode role configs
		c := pb.NewClusnodeClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r, err := c.GetConfigs(ctx, &pb.Empty{})
		print_result(label_clusnode_config, r.GetConfigs(), err)

		// Get headnode role configs
		c_headnode := pb.NewHeadnodeClient(conn)
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r, err = c_headnode.GetConfigs(ctx, &pb.Empty{})
		print_result(label_headnode_config, r.GetConfigs(), err)
	}
}
