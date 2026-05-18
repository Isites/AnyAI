package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Isites/anyai/internal/registry"
	runtimemcp "github.com/Isites/anyai/internal/runtime/mcp"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var npmInstallPackage = func(dir, packageSpec string) error {
	cmd := exec.Command("npm", "install", "--no-audit", "--no-fund", packageSpec)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP servers for an AnyAI project",
	}
	cmd.AddCommand(
		mcpInstallCmd(),
		mcpListCmd(),
	)
	return cmd
}

func mcpInstallCmd() *cobra.Command {
	var projectPath string
	var scope string
	var agentID string
	var transportType string
	var command string
	var args []string
	var env []string
	var url string
	var headers []string
	var description string
	var root string
	var startupTimeoutMS int
	var toolTimeoutMS int
	var force bool
	var noInstallDeps bool

	cmd := &cobra.Command{
		Use:   "install <name>",
		Short: "Install an MCP server config into a project scope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, values []string) error {
			target, err := projectPathOrCWD(projectPath)
			if err != nil {
				return err
			}
			envMap, err := parseKeyValueFlags(env, "env")
			if err != nil {
				return err
			}
			headerMap, err := parseKeyValueFlags(headers, "header")
			if err != nil {
				return err
			}
			server := runtimemcp.ServerConfig{
				Name:             values[0],
				Description:      description,
				Type:             transportType,
				Command:          command,
				Args:             append([]string(nil), args...),
				Env:              envMap,
				URL:              url,
				Headers:          headerMap,
				Root:             root,
				StartupTimeoutMS: startupTimeoutMS,
				ToolTimeoutMS:    toolTimeoutMS,
			}
			return runMCPInstallWithOptions(target, scope, agentID, server, mcpInstallOptions{
				Force:       force,
				InstallDeps: !noInstallDeps,
			})
		},
	}
	cmd.Flags().StringVar(&projectPath, "project", "", "path to the AnyAI project root or agent.md")
	cmd.Flags().StringVar(&scope, "scope", "common", "install scope: common or agent")
	cmd.Flags().StringVar(&agentID, "agent", "", "agent id for --scope agent")
	cmd.Flags().StringVar(&transportType, "type", runtimemcp.TransportStdio, "transport type: stdio, sse, or streamable_http")
	cmd.Flags().StringVar(&command, "command", "", "stdio command to run")
	cmd.Flags().StringArrayVar(&args, "arg", nil, "stdio command argument; repeat for multiple values")
	cmd.Flags().StringArrayVar(&env, "env", nil, "environment variable KEY=VALUE; repeat for multiple values")
	cmd.Flags().StringVar(&url, "url", "", "HTTP/SSE MCP endpoint URL")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "HTTP header KEY=VALUE; repeat for multiple values")
	cmd.Flags().StringVar(&description, "description", "", "description for this MCP server")
	cmd.Flags().StringVar(&root, "root", "", "client root exposed to the MCP server")
	cmd.Flags().IntVar(&startupTimeoutMS, "startup-timeout-ms", 0, "stdio MCP startup timeout in milliseconds")
	cmd.Flags().IntVar(&toolTimeoutMS, "tool-timeout-ms", 0, "MCP tool call timeout in milliseconds")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing MCP config")
	cmd.Flags().BoolVar(&noInstallDeps, "no-install-deps", false, "write config without preinstalling local MCP dependencies")
	return cmd
}

func mcpListCmd() *cobra.Command {
	var projectPath string
	var agentID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers for a project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := projectPathOrCWD(projectPath)
			if err != nil {
				return err
			}
			return runMCPList(target, agentID)
		},
	}
	cmd.Flags().StringVar(&projectPath, "project", "", "path to the AnyAI project root or agent.md")
	cmd.Flags().StringVar(&agentID, "agent", "", "show effective MCP servers for one agent")
	return cmd
}

func runMCPInstall(projectPath, scope, agentID string, server runtimemcp.ServerConfig, force bool) error {
	return runMCPInstallWithOptions(projectPath, scope, agentID, server, mcpInstallOptions{Force: force})
}

type mcpInstallOptions struct {
	Force       bool
	InstallDeps bool
}

func runMCPInstallWithOptions(projectPath, scope, agentID string, server runtimemcp.ServerConfig, opts mcpInstallOptions) error {
	project, err := registry.LoadProject(projectPath)
	if err != nil {
		return err
	}
	server.Name = strings.TrimSpace(server.Name)
	if server.Name == "" {
		return fmt.Errorf("mcp name is required")
	}
	scope = normalizeMCPScope(scope)

	dir, err := mcpInstallDir(project, scope, agentID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create mcp dir: %w", err)
	}

	server.Type = strings.TrimSpace(server.Type)
	if server.Type == "" {
		server.Type = runtimemcp.TransportStdio
	}
	if err := validateMCPInstallConfig(server); err != nil {
		return err
	}

	dst := filepath.Join(dir, safeFileName(server.Name)+".yaml")
	if !opts.Force {
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("mcp config already exists: %s", dst)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if opts.InstallDeps {
		updated, installed, err := installMCPDependencies(project, dir, server)
		if err != nil {
			return err
		}
		server = updated
		if installed {
			fmt.Printf("Installed MCP dependencies for %q into %s\n", server.Name, mcpDependencyDir(project, server.Name))
		}
	}

	data, err := yaml.Marshal(server)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write mcp config: %w", err)
	}
	fmt.Printf("Installed MCP %q into %s scope at %s\n", server.Name, scope, dst)
	return nil
}

func runMCPList(projectPath, agentID string) error {
	project, err := registry.LoadProject(projectPath)
	if err != nil {
		return err
	}
	catalog, err := runtimemcp.BuildCatalog(project.Config)
	if err != nil {
		return err
	}

	var servers []runtimemcp.ServerConfig
	if strings.TrimSpace(agentID) != "" {
		if _, ok := project.Config.GetAgent(agentID); !ok {
			return fmt.Errorf("agent %q not found", agentID)
		}
		servers = catalog.ServersForAgent(agentID)
	} else {
		servers = append(servers, catalog.SharedServers()...)
		for _, agent := range project.Config.Agents.List {
			servers = append(servers, catalog.ServersForAgent(agent.ID)...)
		}
		servers = uniqueServers(servers)
	}

	if len(servers) == 0 {
		fmt.Println("No MCP servers configured.")
		return nil
	}
	sort.SliceStable(servers, func(i, j int) bool {
		if servers[i].Scope != servers[j].Scope {
			return servers[i].Scope < servers[j].Scope
		}
		return servers[i].Name < servers[j].Name
	})
	for _, server := range servers {
		target := server.Command
		if target == "" {
			target = server.URL
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", server.Scope, server.Name, server.TransportType(), target)
	}
	return nil
}

func mcpInstallDir(project *registry.Project, scope, agentID string) (string, error) {
	if project == nil {
		return "", fmt.Errorf("project is not loaded")
	}
	switch scope {
	case "common", runtimemcp.ScopeShared:
		return filepath.Join(project.RootDir, "common", "mcps"), nil
	case "agent", runtimemcp.ScopePrivate:
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			selected, err := project.SelectEntry("")
			if err != nil {
				return "", fmt.Errorf("--agent is required for agent scope: %w", err)
			}
			agentID = selected.ID
		}
		for _, agent := range project.Agents {
			if agent.ID == agentID {
				return filepath.Join(agent.Dir, "mcps"), nil
			}
		}
		return "", fmt.Errorf("agent %q not found", agentID)
	default:
		return "", fmt.Errorf("unsupported scope %q; use common or agent", scope)
	}
}

func validateMCPInstallConfig(server runtimemcp.ServerConfig) error {
	switch server.TransportType() {
	case runtimemcp.TransportStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("--command is required for stdio MCP servers")
		}
	case runtimemcp.TransportSSE, runtimemcp.TransportStreamable:
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("--url is required for %s MCP servers", server.TransportType())
		}
	default:
		return fmt.Errorf("unsupported MCP transport type %q", server.Type)
	}
	if server.StartupTimeoutMS < 0 {
		return fmt.Errorf("--startup-timeout-ms must be >= 0")
	}
	if server.ToolTimeoutMS < 0 {
		return fmt.Errorf("--tool-timeout-ms must be >= 0")
	}
	return nil
}

func installMCPDependencies(project *registry.Project, configDir string, server runtimemcp.ServerConfig) (runtimemcp.ServerConfig, bool, error) {
	if project == nil {
		return server, false, fmt.Errorf("project is not loaded")
	}
	if server.TransportType() != runtimemcp.TransportStdio {
		return server, false, nil
	}
	if !isNPXCommand(server.Command) {
		return server, false, nil
	}

	packageSpec, runtimeArgs, ok := splitNPXArgs(server.Args)
	if !ok {
		return server, false, fmt.Errorf("npx MCP server %q must include a package name in --arg", server.Name)
	}

	depDir := mcpDependencyDir(project, server.Name)
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		return server, false, fmt.Errorf("create mcp dependency dir: %w", err)
	}
	if err := ensureMCPDependencyPackage(depDir, server.Name); err != nil {
		return server, false, err
	}
	if err := npmInstallPackage(depDir, packageSpec); err != nil {
		return server, false, fmt.Errorf("install npm MCP dependency %q: %w", packageSpec, err)
	}

	binName, err := resolveNPXBin(depDir, packageSpec)
	if err != nil {
		return server, false, err
	}
	binPath, err := resolveMCPBinPath(depDir, binName)
	if err != nil {
		return server, false, err
	}

	updated := server.Clone()
	updated.Command = commandPathForConfig(configDir, binPath)
	updated.Args = runtimeArgs
	return updated, true, nil
}

func mcpDependencyDir(project *registry.Project, serverName string) string {
	if project == nil {
		return ""
	}
	return filepath.Join(project.RootDir, "anyai", "mcps", safeFileName(serverName))
}

func isNPXCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(command))
	return base == "npx" || base == "npx.cmd"
}

func splitNPXArgs(args []string) (string, []string, bool) {
	packageSpec := ""
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		if arg == "--" {
			if i+1 >= len(args) {
				return "", nil, false
			}
			spec := strings.TrimSpace(args[i+1])
			if spec == "" {
				return "", nil, false
			}
			return spec, cloneRuntimeArgs(args[i+2:]), true
		}
		if strings.HasPrefix(arg, "--package=") {
			packageSpec = strings.TrimSpace(strings.TrimPrefix(arg, "--package="))
			continue
		}
		if arg == "-p" || arg == "--package" {
			if i+1 >= len(args) {
				return "", nil, false
			}
			packageSpec = strings.TrimSpace(args[i+1])
			i++
			continue
		}
		if isNPXOptionWithValue(arg) {
			i++
			continue
		}
		if isNPXInstallFlag(arg) || strings.HasPrefix(arg, "-") {
			continue
		}
		if packageSpec != "" {
			return packageSpec, cloneRuntimeArgs(args[i+1:]), packageSpec != ""
		}
		return arg, cloneRuntimeArgs(args[i+1:]), true
	}
	if packageSpec == "" {
		return "", nil, false
	}
	return packageSpec, nil, true
}

func isNPXInstallFlag(arg string) bool {
	switch arg {
	case "-y", "--yes", "--no-install", "--ignore-existing", "--quiet", "-q":
		return true
	default:
		return false
	}
}

func isNPXOptionWithValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "--cache", "--registry", "--userconfig", "--prefix", "--script-shell", "--shell", "--workspace", "-w":
		return true
	default:
		return false
	}
}

func cloneRuntimeArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			out = append(out, arg)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ensureMCPDependencyPackage(depDir, serverName string) error {
	path := filepath.Join(depDir, "package.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat mcp dependency package.json: %w", err)
	}

	name := "anyai-mcp-" + strings.ReplaceAll(safeFileName(serverName), "_", "-")
	data, err := json.MarshalIndent(map[string]any{
		"name":    name,
		"private": true,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write mcp dependency package.json: %w", err)
	}
	return nil
}

func resolveNPXBin(depDir, packageSpec string) (string, error) {
	packageName := packageNameFromNPXSpec(packageSpec)
	if packageName != "" {
		packageJSONPath := filepath.Join(depDir, "node_modules", filepath.FromSlash(packageName), "package.json")
		if data, err := os.ReadFile(packageJSONPath); err == nil {
			if binName, ok, err := binNameFromPackageJSON(data, packageName); err != nil {
				return "", err
			} else if ok {
				return binName, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read installed package metadata: %w", err)
		}
		if baseName := packageBaseName(packageName); baseName != "" {
			if _, err := os.Stat(filepath.Join(depDir, "node_modules", ".bin", baseName)); err == nil {
				return baseName, nil
			}
		}
	}

	bins, err := installedBinNames(depDir)
	if err != nil {
		return "", err
	}
	if baseName := packageBaseName(packageName); baseName != "" {
		for _, bin := range bins {
			if bin == baseName {
				return bin, nil
			}
		}
	}
	if len(bins) == 1 {
		return bins[0], nil
	}
	if len(bins) > 1 {
		return "", fmt.Errorf("multiple npm binaries installed for %q; cannot choose one automatically: %s", packageSpec, strings.Join(bins, ", "))
	}
	return "", fmt.Errorf("no npm binary found for MCP dependency %q", packageSpec)
}

func packageNameFromNPXSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasPrefix(spec, "-") {
		return ""
	}
	if strings.Contains(spec, "://") ||
		strings.HasPrefix(spec, "file:") ||
		strings.HasPrefix(spec, "git+") ||
		strings.HasPrefix(spec, "github:") {
		return ""
	}
	if strings.HasPrefix(spec, "npm:") {
		spec = strings.TrimPrefix(spec, "npm:")
	}
	if strings.HasPrefix(spec, "@") {
		slash := strings.Index(spec, "/")
		if slash <= 1 || slash == len(spec)-1 {
			return ""
		}
		scope := spec[:slash]
		rest := spec[slash+1:]
		if at := strings.LastIndex(rest, "@"); at > 0 {
			rest = rest[:at]
		}
		if rest == "" || strings.ContainsAny(rest, `/\`) {
			return ""
		}
		return scope + "/" + rest
	}
	if alias, _, ok := strings.Cut(spec, "@npm:"); ok {
		return alias
	}
	if at := strings.Index(spec, "@"); at > 0 {
		spec = spec[:at]
	}
	if spec == "" || strings.ContainsAny(spec, `/\`) {
		return ""
	}
	return spec
}

func packageBaseName(packageName string) string {
	packageName = strings.TrimSpace(packageName)
	if packageName == "" {
		return ""
	}
	if strings.HasPrefix(packageName, "@") {
		_, name, ok := strings.Cut(packageName, "/")
		if ok {
			return name
		}
	}
	return packageName
}

func binNameFromPackageJSON(data []byte, packageName string) (string, bool, error) {
	var pkg struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", false, fmt.Errorf("parse installed package metadata: %w", err)
	}
	if len(pkg.Bin) == 0 || string(pkg.Bin) == "null" {
		return "", false, nil
	}

	var single string
	if err := json.Unmarshal(pkg.Bin, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			return "", false, nil
		}
		baseName := packageBaseName(packageName)
		if baseName == "" {
			return "", false, fmt.Errorf("cannot infer binary name for package %q", packageName)
		}
		return baseName, true, nil
	}

	var bins map[string]string
	if err := json.Unmarshal(pkg.Bin, &bins); err != nil {
		return "", false, fmt.Errorf("parse installed package bin metadata: %w", err)
	}
	if len(bins) == 0 {
		return "", false, nil
	}
	baseName := packageBaseName(packageName)
	if baseName != "" {
		if _, ok := bins[baseName]; ok {
			return baseName, true, nil
		}
	}
	names := make([]string, 0, len(bins))
	for name := range bins {
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", false, nil
	}
	return names[0], true, nil
}

func installedBinNames(depDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(depDir, "node_modules", ".bin"))
	if err != nil {
		return nil, fmt.Errorf("read npm binary dir: %w", err)
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".cmd", ".ps1", ".exe":
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		seen[name] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func resolveMCPBinPath(depDir, binName string) (string, error) {
	binDir := filepath.Join(depDir, "node_modules", ".bin")
	candidates := []string{binName, binName + ".cmd", binName + ".exe", binName + ".ps1"}
	for _, candidate := range candidates {
		path := filepath.Join(binDir, candidate)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat npm binary: %w", err)
		}
	}
	return "", fmt.Errorf("npm binary %q was not linked in %s", binName, binDir)
}

func commandPathForConfig(configDir, commandPath string) string {
	configDir = strings.TrimSpace(configDir)
	commandPath = strings.TrimSpace(commandPath)
	if configDir == "" || commandPath == "" {
		return commandPath
	}
	if rel, err := filepath.Rel(configDir, commandPath); err == nil && strings.TrimSpace(rel) != "" {
		return filepath.Clean(rel)
	}
	return commandPath
}

func normalizeMCPScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "common", "shared":
		return "common"
	case "agent", "private":
		return "agent"
	default:
		return strings.ToLower(strings.TrimSpace(scope))
	}
}

func parseKeyValueFlags(values []string, flagName string) (map[string]string, error) {
	out := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("--%s must be KEY=VALUE, got %q", flagName, value)
		}
		out[key] = strings.TrimSpace(val)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func uniqueServers(servers []runtimemcp.ServerConfig) []runtimemcp.ServerConfig {
	out := make([]runtimemcp.ServerConfig, 0, len(servers))
	seen := map[string]struct{}{}
	for _, server := range servers {
		key := server.Scope + "\x00" + strings.ToLower(server.Name) + "\x00" + server.Source
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, server)
	}
	return out
}

func safeFileName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "mcp"
	}
	return out
}
