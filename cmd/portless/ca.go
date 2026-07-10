package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sanketsudake/go-portless/ca"
	"golang.org/x/term"
)

func cmdCA(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("ca: expected subcommand: path | install | uninstall")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "path":
		return caPath(rest, stdout, stderr)
	case "install":
		return caInstall(rest, stdout, stderr)
	case "uninstall":
		return caUninstall(rest, stdout, stderr)
	default:
		return fmt.Errorf("ca: unknown subcommand %q", sub)
	}
}

func caStateDir(fs *flag.FlagSet) *string {
	return fs.String("state-dir", ca.DefaultStateDir(), "CA/state directory")
}

// caPath ensures the CA exists and prints its certificate path, for manual
// trust (curl --cacert, NODE_EXTRA_CA_CERTS, browser import).
func caPath(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ca path", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := caStateDir(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := ca.Load(*stateDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, filepath.Join(*stateDir, "ca.crt"))
	return nil
}

// caInstall adds the CA to the OS trust store. It modifies system state, so it
// confirms first (skip with --yes) and refuses in a non-interactive shell
// without --yes.
func caInstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ca install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := caStateDir(fs)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := ca.Load(*stateDir); err != nil {
		return err
	}
	crt := filepath.Join(*stateDir, "ca.crt")

	ok, err := confirm(stdout, fmt.Sprintf("Add the portless CA (%s) to your OS trust store? This changes system trust and may prompt for your password.", crt), *yes)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(stdout, "aborted")
		return nil
	}
	if err := trustInstall(crt); err != nil {
		fmt.Fprintf(stderr, "portless: automatic install failed: %v\n\n", err)
		printManualTrust(stderr, crt)
		return errors.New("ca install: could not update the trust store automatically")
	}
	fmt.Fprintln(stdout, "portless CA installed. https://<name> routes now verify.")
	return nil
}

func caUninstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ca uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := caStateDir(fs)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	crt := filepath.Join(*stateDir, "ca.crt")
	ok, err := confirm(stdout, "Remove the portless CA from your OS trust store?", *yes)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(stdout, "aborted")
		return nil
	}
	if err := trustUninstall(crt); err != nil {
		return fmt.Errorf("ca uninstall: %w", err)
	}
	fmt.Fprintln(stdout, "portless CA removed.")
	return nil
}

// confirm prompts on stdin unless assumeYes. In a non-interactive shell it
// refuses rather than silently proceeding.
func confirm(stdout io.Writer, prompt string, assumeYes bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errors.New("not a terminal; re-run with --yes to confirm")
	}
	fmt.Fprintf(stdout, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

// trustInstall adds crt to the platform trust store.
func trustInstall(crt string) error {
	switch runtime.GOOS {
	case "darwin":
		return execCmd("security", "add-trusted-cert", "-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain", crt)
	case "linux":
		dst := "/usr/local/share/ca-certificates/portless.crt"
		if err := execCmd("sudo", "cp", crt, dst); err != nil {
			return err
		}
		return execCmd("sudo", "update-ca-certificates")
	default:
		return fmt.Errorf("automatic install not supported on %s", runtime.GOOS)
	}
}

func trustUninstall(crt string) error {
	switch runtime.GOOS {
	case "darwin":
		return execCmd("security", "remove-trusted-cert", "-d", crt)
	case "linux":
		if err := execCmd("sudo", "rm", "-f", "/usr/local/share/ca-certificates/portless.crt"); err != nil {
			return err
		}
		return execCmd("sudo", "update-ca-certificates", "--fresh")
	default:
		return fmt.Errorf("automatic uninstall not supported on %s", runtime.GOOS)
	}
}

func printManualTrust(w io.Writer, crt string) {
	fmt.Fprintf(w, "Trust the CA manually:\n")
	switch runtime.GOOS {
	case "darwin":
		fmt.Fprintf(w, "  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s\n", crt)
	case "linux":
		fmt.Fprintf(w, "  sudo cp %s /usr/local/share/ca-certificates/portless.crt && sudo update-ca-certificates\n", crt)
	}
	fmt.Fprintf(w, "Or point tools at it directly: curl --cacert %s / NODE_EXTRA_CA_CERTS=%s\n", crt, crt)
}

func execCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	return cmd.Run()
}
