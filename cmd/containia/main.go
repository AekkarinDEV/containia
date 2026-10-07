package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"containia/pkg/builder"
	"containia/pkg/config"
	"containia/pkg/dashboard"
	"containia/pkg/image"
	"containia/pkg/runtime"
)

const version = "0.2.0-beta (OCI-enabled)"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "child":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: containia child <id> <cmd> [args...]\n")
			os.Exit(1)
		}
		containerID := os.Args[2]
		userCmd := os.Args[3:]
		if err := runtime.Child(containerID, userCmd); err != nil {
			fmt.Fprintf(os.Stderr, "Child error: %v\n", err)
			os.Exit(1)
		}

	case "run":
		handleRun(os.Args[2:])

	case "ps":
		handlePS(os.Args[2:])

	case "images":
		handleImages()

	case "pull":
		if len(os.Args) < 3 {
			fmt.Println("Usage: containia pull <image>")
			os.Exit(1)
		}
		if err := image.Pull(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "rmi":
		if len(os.Args) < 3 {
			fmt.Println("Usage: containia rmi <image>")
			os.Exit(1)
		}
		if err := image.Remove(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "build":
		handleBuild(os.Args[2:])

	case "stop":
		if len(os.Args) < 3 {
			fmt.Println("Usage: containia stop <container>")
			os.Exit(1)
		}
		if err := runtime.Stop(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "rm":
		handleRM(os.Args[2:])

	case "logs":
		if len(os.Args) < 3 {
			fmt.Println("Usage: containia logs <container>")
			os.Exit(1)
		}
		if err := runtime.Logs(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "exec":
		handleExec(os.Args[2:])

	case "ui", "dashboard":
		handleUI(os.Args[2:])

	case "version":
		fmt.Printf("containia version %s\n", version)

	case "help", "--help", "-h":
		printHelp()

	default:
		fmt.Fprintf(os.Stderr, "containia: '%s' is not a containia command.\nSee 'containia --help'\n", command)
		os.Exit(1)
	}
}

func handleUI(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	port := fs.Int("port", 8080, "Dashboard HTTP port")
	fs.IntVar(port, "p", 8080, "Dashboard HTTP port")
	_ = fs.Parse(args)

	server := dashboard.NewServer(*port)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Dashboard error: %v\n", err)
		os.Exit(1)
	}
}

func handleRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)

	var flags config.RunFlags
	var envList stringSliceFlag
	var volList stringSliceFlag
	var portList stringSliceFlag

	fs.StringVar(&flags.Name, "name", "", "Assign a name to the container")
	fs.StringVar(&flags.Memory, "m", "", "Memory limit (e.g., 128m, 512m, 1g)")
	fs.StringVar(&flags.Memory, "memory", "", "Memory limit (e.g., 128m, 512m, 1g)")
	fs.StringVar(&flags.CPUs, "cpus", "", "Number of CPUs (e.g., 0.5, 1.0)")
	fs.Int64Var(&flags.PidsLimit, "pids-limit", 1000, "Tune container pids limit (default 1000)")
	fs.BoolVar(&flags.Interactive, "i", false, "Keep STDIN open even if not attached")
	fs.BoolVar(&flags.Tty, "t", false, "Allocate a pseudo-TTY")
	fs.BoolVar(&flags.Detach, "d", false, "Run container in background and print container ID")
	fs.BoolVar(&flags.Remove, "rm", false, "Automatically remove the container when it exits")
	fs.StringVar(&flags.Network, "net", "bridge", "Network mode ('bridge', 'none')")
	fs.StringVar(&flags.WorkingDir, "w", "", "Working directory inside the container")
	fs.StringVar(&flags.WorkingDir, "workdir", "", "Working directory inside the container")
	fs.Var(&portList, "p", "Publish a container's port(s) to the host (hostPort:containerPort)")
	fs.Var(&portList, "publish", "Publish a container's port(s) to the host (hostPort:containerPort)")
	fs.Var(&envList, "e", "Set environment variables (can be used multiple times)")
	fs.Var(&envList, "env", "Set environment variables (can be used multiple times)")
	fs.Var(&volList, "v", "Bind mount a volume (host:container)")
	fs.Var(&volList, "volume", "Bind mount a volume (host:container)")

	_ = fs.Parse(args)
	flags.Env = envList
	flags.Volumes = volList
	flags.Ports = portList

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: containia run [OPTIONS] IMAGE [COMMAND] [ARG...]")
		os.Exit(1)
	}

	imageName := remaining[0]
	cmdArgs := remaining[1:]

	if err := runtime.Run(flags, imageName, cmdArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func handleBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	tag := fs.String("t", "", "Name and optionally a tag in the 'name:tag' format")
	dockerfile := fs.String("f", "", "Name of the Dockerfile (Default is 'PATH/Dockerfile')")
	_ = fs.Parse(args)

	contextDir := "."
	if len(fs.Args()) > 0 {
		contextDir = fs.Args()[0]
	}

	targetTag := *tag
	if targetTag == "" {
		targetTag = "containia-image:latest"
	}

	if err := builder.Build(contextDir, *dockerfile, targetTag); err != nil {
		fmt.Fprintf(os.Stderr, "Build error: %v\n", err)
		os.Exit(1)
	}
}

func handlePS(args []string) {
	fs := flag.NewFlagSet("ps", flag.ExitOnError)
	all := fs.Bool("a", false, "Show all containers (default shows just running)")
	_ = fs.Parse(args)

	if err := runtime.PS(*all); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func handleImages() {
	list, err := image.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tTAG\tIMAGE ID\tSIZE")

	for _, img := range list {
		sizeMB := float64(img.Size) / (1024 * 1024)
		fmt.Fprintf(w, "%s\t%s\t%s\t%.2fMB\n",
			img.Repository,
			img.Tag,
			img.ID,
			sizeMB,
		)
	}
	_ = w.Flush()
}

func handleRM(args []string) {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	force := fs.Bool("f", false, "Force the removal of a running container")
	_ = fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: containia rm [-f] <container>")
		os.Exit(1)
	}

	if err := runtime.RM(remaining[0], *force); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func handleExec(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: containia exec <container> <command> [args...]")
		os.Exit(1)
	}

	var cleanArgs []string
	for _, arg := range args {
		if arg != "-it" && arg != "-i" && arg != "-t" {
			cleanArgs = append(cleanArgs, arg)
		}
	}

	if len(cleanArgs) < 2 {
		fmt.Println("Usage: containia exec <container> <command> [args...]")
		os.Exit(1)
	}

	containerID := cleanArgs[0]
	command := cleanArgs[1:]

	if err := runtime.Exec(containerID, command); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	helpText := `
Containia - A Mini Docker/Podman-compatible Container Runtime built from scratch

Usage:
  containia [command] [options]

Image Management Commands:
  pull        Pull an image from Docker Registry v2 / OCI (e.g. alpine, postgres:18)
  build       Build an image from a Dockerfile (containia build -t <tag> <context>)
  images      List local images
  rmi         Remove one or more images and free unused layers

Container Management Commands:
  run         Run a command in a new container
  ps          List containers
  stop        Stop one or more running containers
  rm          Remove one or more stopped containers
  logs        Fetch the logs of a container
  exec        Run a command in a running container
  ui          Start the Web Monitor GUI (containia ui [-p 8080])
  version     Show the containia version information

Run Options:
  -i, -t             Interactive shell
  -d                 Run container in background (detached mode)
  -m, --memory       Memory limit (e.g., 128m, 512m, 1g)
  --cpus             CPU limit (e.g., 0.5, 1.0)
  --pids-limit       PID limit to protect against fork-bombs (default 100)
  --name             Assign a friendly name to the container
  --rm               Automatically remove the container when it exits
  --net              Network mode ('bridge', 'none')
  -w, --workdir      Working directory inside the container
  -v, --volume       Bind mount host directory into container (host:container)
  -e, --env          Set environment variables

Examples:
  containia pull alpine
  containia pull postgres:18
  containia build -t my-db:1.0 ./Project/db
  containia run -it alpine /bin/sh
  containia run -d --name db -m 256m postgres:18
  containia ps
  containia stop db
  containia rmi my-db:1.0
`
	fmt.Println(strings.TrimSpace(helpText))
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(val string) error {
	*s = append(*s, val)
	return nil
}
