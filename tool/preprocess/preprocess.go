// Copyright (c) 2024 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package preprocess

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/alibaba/loongsuite-go-agent/tool/config"
	"github.com/alibaba/loongsuite-go-agent/tool/errc"
	"github.com/alibaba/loongsuite-go-agent/tool/resource"
	"github.com/alibaba/loongsuite-go-agent/tool/util"
	"github.com/dave/dst"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// -----------------------------------------------------------------------------
// Preprocess
//
// The preprocess package is used to preprocess the source code before the actual
// instrumentation. Instrumentation rules may introduces additional dependencies
// that are not present in original source code. The preprocess is responsible
// for preparing these dependencies in advance.

const (
	OtelImporter     = "otel_importer.go"
	OtelRuleCache    = "rule_cache"
	OtelBackups      = "backups"
	OtelBackupSuffix = ".bk"
	DryRunLog        = "dry_run.log"
	CompileRemix     = "remix"
	VendorDir        = "vendor"
	GoCacheDir       = "gocache"
)

type DepProcessor struct {
	backups       map[string]string
	moduleName    string // Module name from go.mod
	modulePath    string // Where go.mod is located
	goBuildCmd    []string
	vendorMode    bool
	pkgLocalCache string // Local module cache path of alibaba-otel pkg module
	otelImporter  string // Path to the otel_importer.go file
}

func newDepProcessor() *DepProcessor {
	dp := &DepProcessor{
		backups:       map[string]string{},
		vendorMode:    false,
		pkgLocalCache: "",
		otelImporter:  "",
	}
	return dp
}

func (dp *DepProcessor) String() string {
	return fmt.Sprintf("moduleName: %s, modulePath: %s, goBuildCmd: %v, vendorMode: %v, pkgLocalCache: %s, otelImporter: %s",
		dp.moduleName, dp.modulePath, dp.goBuildCmd, dp.vendorMode,
		dp.pkgLocalCache, dp.otelImporter)
}

func (dp *DepProcessor) getGoModPath() string {
	util.Assert(dp.modulePath != "", "modulePath is empty")
	return dp.modulePath
}

func (dp *DepProcessor) getGoModDir() string {
	return filepath.Dir(dp.getGoModPath())
}

func (dp *DepProcessor) generatedOf(dir string) string {
	return filepath.Join(dp.getGoModDir(), dir)
}

// Run runs the command and returns the combined standard output and standard
// error. dir specifies the working directory of the command. If dir is the
// empty string, run runs the command in the calling process's current directory.
func runCmdCombinedOutput(dir string, env []string, args ...string) (string, error) {
	path := args[0]
	args = args[1:]
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errc.New(errc.ErrRunCmd, string(out)).
			With("command", fmt.Sprintf("%v", args))
	}
	return string(out), nil
}

// Find go.mod from dir and its parent recursively
func findGoMod(dir string) (string, error) {
	for dir != "" {
		mod := filepath.Join(dir, util.GoModFile)
		if util.PathExists(mod) {
			return mod, nil
		}
		par := filepath.Dir(dir)
		if par == dir {
			break
		}
		dir = par
	}
	return "", errc.New(errc.ErrPreprocess, "cannot find go.mod")
}

func parseGoMod(gomod string) (*modfile.File, error) {
	data, err := util.ReadFile(gomod)
	if err != nil {
		return nil, err
	}
	modFile, err := modfile.Parse(util.GoModFile, []byte(data), nil)
	if err != nil {
		return nil, errc.New(errc.ErrParseCode, err.Error())
	}
	return modFile, nil
}

func (dp *DepProcessor) initCmd() {
	// There is a tricky, all arguments after the otel tool itself are saved for
	// later use, which means the subcommand "go build" itself are also included
	dp.goBuildCmd = make([]string, len(os.Args)-1)
	copy(dp.goBuildCmd, os.Args[1:])
	util.AssertGoBuild(dp.goBuildCmd)
}

func findMainDir(pkgs []*packages.Package) (string, error) {
	gofiles := make([]string, 0)
	for _, pkg := range pkgs {
		if pkg.GoFiles == nil {
			continue
		}
		gofiles = append(gofiles, pkg.GoFiles...)
	}
	for _, gofile := range gofiles {
		if !util.IsGoFile(gofile) {
			continue
		}
		root, err := util.ParseAstFromFileFast(gofile)
		if err != nil {
			return "", err
		}
		for _, decl := range root.Decls {
			if d, ok := decl.(*dst.FuncDecl); ok && d.Name.Name == "main" {
				// We found the main function, return the directory of the file
				return filepath.Dir(gofile), nil
			}
		}
	}
	return "", errc.New(errc.ErrPreprocess,
		"cannot find main function in the source files")
}

func (dp *DepProcessor) initMod() (err error) {
	// Find compiling module and package information from the build command
	pkgs, err := findModule(dp.goBuildCmd)
	if err != nil {
		return err
	}
	util.Log("Find Go packages %v", util.Jsonify(pkgs))
	for _, pkg := range pkgs {
		util.Log("Find Go package %v", util.Jsonify(pkg))
		if pkg.GoFiles == nil {
			continue
		}
		if pkg.Module != nil {
			// Build the module
			// Best case, we find the module information from the package field
			util.Log("Find Go module %v", util.Jsonify(pkg.Module))
			util.Assert(pkg.Module.Path != "", "pkg.Module.Path is empty")
			util.Assert(pkg.Module.GoMod != "", "pkg.Module.GoMod is empty")
			dp.moduleName = pkg.Module.Path
			dp.modulePath = pkg.Module.GoMod
			dir, err := findMainDir(pkgs)
			if err != nil {
				return err
			}
			dp.otelImporter = filepath.Join(dir, OtelImporter)
		} else {
			// Build the source files
			// If we cannot find the module information from the package field,
			// we try to find it from the go.mod file, where go.mod file is in
			// the same directory as the source file.
			util.Assert(pkg.Name != "", "pkg.Name is empty")
			if pkg.Name == "main" {
				gofile := pkg.GoFiles[0]
				gomod, err := findGoMod(filepath.Dir(gofile))
				if err != nil {
					return err
				}
				util.Assert(gomod != "", "gomod is empty")
				util.Assert(util.PathExists(gomod), "gomod does not exist")
				dp.modulePath = gomod
				// Get module name from go.mod file
				modfile, err := parseGoMod(gomod)
				if err != nil {
					return err
				}
				dp.moduleName = modfile.Module.Mod.Path
				// We generate additional source file(otel_importer.go) in the
				// same directory as the go.mod file, we should append this file
				// into build commands to make sure it is compiled together with
				// the original source files.
				found := false
				for _, cmd := range dp.goBuildCmd {
					if strings.Contains(cmd, OtelImporter) {
						found = true
						break
					}
				}
				if !found {
					last := dp.goBuildCmd[len(dp.goBuildCmd)-1]
					dp.otelImporter = filepath.Join(filepath.Dir(last), OtelImporter)
					dp.goBuildCmd = append(dp.goBuildCmd, dp.otelImporter)
				}
			}
		}
	}
	if dp.moduleName == "" || dp.modulePath == "" {
		return errc.New(errc.ErrPreprocess, "cannot find compiled module")
	}
	if dp.otelImporter == "" {
		return errc.New(errc.ErrPreprocess, "cannot place otel_importer.go file")
	}

	// We will import alibaba-otel/pkg module in generated code, which is not
	// published yet, so we also need to add a replace directive to the go.mod file
	// to tell the go tool to use the local module cache instead of the remote
	// module, that's why we do this here.
	// TODO: Once we publish the alibaba-otel/pkg module, we can remove this code
	// along with the replace directive in the go.mod file.
	dp.pkgLocalCache, err = findModCacheDir()
	if err != nil {
		return err
	}
	// In the further processing, we will edit the go.mod file, which is illegal
	// to use relative path, so we need to convert the relative path to an absolute
	dp.pkgLocalCache, err = filepath.Abs(dp.pkgLocalCache)
	if err != nil {
		return err
	}
	if dp.pkgLocalCache == "" {
		return errc.New(errc.ErrPreprocess, "cannot find rule cache dir")
	}
	return nil
}

func (dp *DepProcessor) initBuildMode() {
	// Check if the build mode
	ignoreVendor := false
	for _, arg := range dp.goBuildCmd {
		// -mod=mod and -mod=readonly tells the go command to ignore the vendor
		// directory. We should not use the vendor directory in this case.
		if strings.HasPrefix(arg, "-mod=mod") ||
			strings.HasPrefix(arg, "-mod=readonly") {
			dp.vendorMode = false
			ignoreVendor = true
			break
		}
	}
	if !ignoreVendor {
		// FIXME: vendor directory name can be anything, but we assume it's "vendor"
		// for now
		vendor := filepath.Join(dp.getGoModDir(), VendorDir)
		dp.vendorMode = util.PathExists(vendor)
	}
	// If we are building with vendored dependencies, we should not pull any
	// additional dependencies online, which means all dependencies should be
	// available in the vendor directory. This requires users to add these
	// dependencies proactively
}

func (dp *DepProcessor) initSignalHandler() {
	// Register signal handler to catch up SIGINT/SIGTERM interrupt signals and
	// do necessary cleanup
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigc
		switch s {
		case syscall.SIGTERM, syscall.SIGINT:
			util.Log("Interrupted instrumentation, cleaning up")
		default:
		}
	}()
}

func (dp *DepProcessor) init() error {
	dp.initCmd()
	err := dp.initMod()
	if err != nil {
		return err
	}
	dp.initBuildMode()
	dp.initSignalHandler()
	// Once all the initialization is done, let's log the configuration
	util.Log("ToolVersion: %s", config.ToolVersion)
	util.Log("%s", dp.String())
	return nil
}

func (dp *DepProcessor) postProcess() {
	util.GuaranteeInPreprocess()

	// Using -debug? Leave all changes for debugging
	if config.GetConf().Debug {
		return
	}

	_ = os.RemoveAll(dp.otelImporter)
	_ = os.RemoveAll(util.GetTempBuildDirWith("alibaba-pkg"))
	_ = dp.restoreBackupFiles()
}

func (dp *DepProcessor) backupFile(origin string) error {
	util.GuaranteeInPreprocess()
	backup := filepath.Base(origin) + OtelBackupSuffix
	backup = util.GetLogPath(filepath.Join(OtelBackups, backup))
	err := os.MkdirAll(filepath.Dir(backup), 0777)
	if err != nil {
		return errc.New(errc.ErrMkdirAll, err.Error())
	}
	if _, exist := dp.backups[origin]; !exist {
		err = util.CopyFile(origin, backup)
		if err != nil {
			return err
		}
		dp.backups[origin] = backup
		util.Log("Backup %v", origin)
	} else if config.GetConf().Verbose {
		util.Log("Backup %v already exists", origin)
	}
	return nil
}

func (dp *DepProcessor) restoreBackupFiles() error {
	util.GuaranteeInPreprocess()
	for origin, backup := range dp.backups {
		err := util.CopyFile(backup, origin)
		if err != nil {
			return err
		}
		util.Log("Restore %v", origin)
	}
	return nil
}

func getCompileCommands() ([]string, error) {
	dryRunLog, err := os.Open(util.GetLogPath(DryRunLog))
	if err != nil {
		return nil, errc.New(errc.ErrOpenFile, err.Error())
	}
	defer func(dryRunLog *os.File) {
		err := dryRunLog.Close()
		if err != nil {
			util.Log("Failed to close dry run log file: %v", err)
		}
	}(dryRunLog)

	// Filter compile commands from dry run log
	compileCmds := make([]string, 0)
	scanner := bufio.NewScanner(dryRunLog)
	// 10MB should be enough to accommodate most long line
	buffer := make([]byte, 0, 10*1024*1024)
	scanner.Buffer(buffer, cap(buffer))
	for scanner.Scan() {
		line := scanner.Text()
		if util.IsCompileCommand(line) {
			line = strings.Trim(line, " ")
			compileCmds = append(compileCmds, line)
		}
	}
	err = scanner.Err()
	if err != nil {
		return nil, errc.New(errc.ErrParseCode, "cannot parse dry run log")
	}
	return compileCmds, nil
}

// $ go help packages
// Many commands apply to a set of packages:
//
//	go <action> [packages]
//
// Usually, [packages] is a list of import paths.
//
// An import path that is a rooted path or that begins with
// a . or .. element is interpreted as a file system path and
// denotes the package in that directory.
//
// Otherwise, the import path P denotes the package found in
// the directory DIR/src/P for some DIR listed in the GOPATH
// environment variable (For more details see: 'go help gopath').
//
// If no import paths are given, the action applies to the
// package in the current directory.
//
// There are four reserved names for paths that should not be used
// for packages to be built with the go tool:
//
// - "main" denotes the top-level package in a stand-alone executable.
//
// - "all" expands to all packages found in all the GOPATH
// trees. For example, 'go list all' lists all the packages on the local
// system. When using modules, "all" expands to all packages in
// the main module and their dependencies, including dependencies
// needed by tests of any of those.
//
// - "std" is like all but expands to just the packages in the standard
// Go library.
//
// - "cmd" expands to the Go repository's commands and their
// internal libraries.
//
// Import paths beginning with "cmd/" only match source code in
// the Go repository.
//
// An import path is a pattern if it includes one or more "..." wildcards,
// each of which can match any string, including the empty string and
// strings containing slashes. Such a pattern expands to all package
// directories found in the GOPATH trees with names matching the
// patterns.
//
// To make common patterns more convenient, there are two special cases.
// First, /... at the end of the pattern can match an empty string,
// so that net/... matches both net and packages in its subdirectories, like net/http.
// Second, any slash-separated pattern element containing a wildcard never
// participates in a match of the "vendor" element in the path of a vendored
// package, so that ./... does not match packages in subdirectories of
// ./vendor or ./mycode/vendor, but ./vendor/... and ./mycode/vendor/... do.
// Note, however, that a directory named vendor that itself contains code
// is not a vendored package: cmd/vendor would be a command named vendor,
// and the pattern cmd/... matches it.
// See golang.org/s/go15vendor for more about vendoring.
//
// An import path can also name a package to be downloaded from
// a remote repository. Run 'go help importpath' for details.
//
// Every package in a program must have a unique import path.
// By convention, this is arranged by starting each path with a
// unique prefix that belongs to you. For example, paths used
// internally at Google all begin with 'google', and paths
// denoting remote repositories begin with the path to the code,
// such as 'github.com/user/repo'.
//
// Packages in a program need not have unique package names,
// but there are two reserved package names with special meaning.
// The name main indicates a command, not a library.
// Commands are built into binaries and cannot be imported.
// The name documentation indicates documentation for
// a non-Go program in the directory. Files in package documentation
// are ignored by the go command.
//
// As a special case, if the package list is a list of .go files from a
// single directory, the command is applied to a single synthesized
// package made up of exactly those files, ignoring any build constraints
// in those files and ignoring any other files in the directory.
//
// Directory and file names that begin with "." or "_" are ignored
// by the go tool, as are directories named "testdata".

func tryLoadPackage(path string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		// Change it unless you know what you are doing
		Mode: packages.NeedModule | packages.NeedFiles | packages.NeedName,
	}

	pkgs, err := packages.Load(cfg, path)
	if err != nil {
		return nil, errc.New(errc.ErrPreprocess, err.Error())
	}
	return pkgs, nil
}

func findModule(buildCmd []string) ([]*packages.Package, error) {
	candidates := make([]*packages.Package, 0)
	found := false

	// Find from build arguments e.g. go build test.go or go build cmd/app
	for i := len(buildCmd) - 1; i >= 0; i-- {
		buildArg := buildCmd[i]

		// Stop canary when we see a build flag or a "build" command
		if strings.HasPrefix("-", buildArg) ||
			buildArg == "build" ||
			buildArg == "install" {
			break
		}

		// Special case. If the file named with test_ prefix, we create a fake
		// package for it. This is a workaround for the case that the test file
		// is compiled with other normal files.
		if strings.HasSuffix(buildArg, ".go") &&
			strings.HasPrefix(buildArg, "test_") {
			artificialPkg := &packages.Package{
				GoFiles: []string{buildArg},
				Name:    "main",
			}
			candidates = append(candidates, artificialPkg)
			found = true
			continue
		}

		// Trying to load package from the build argument, error is tolerated
		// because we dont know what the build argument is. One exception is
		// when we already found packages, in this case, we expect subsequent
		// build arguments are packages, so we should not tolerate any error.
		pkgs, err := tryLoadPackage(buildArg)
		if err != nil {
			if found {
				// If packages are already found, we expect subsequent build
				// arguments are packages, so we should not tolerate any error
				break
			}
			util.Log("Cannot load package from %v", buildArg)
			continue
		}
		for _, pkg := range pkgs {
			if pkg.Errors != nil {
				continue
			}
			found = true
			candidates = append(candidates, pkg)
		}
	}

	// If no import paths are given, the action applies to the package in the
	// current directory.
	if !found {
		pkgs, err := tryLoadPackage(".")
		if err != nil {
			return nil, err
		}
		for _, pkg := range pkgs {
			if pkg.Errors != nil {
				continue
			}
			candidates = append(candidates, pkg)
		}
	}
	if len(candidates) == 0 {
		return nil, errc.New(errc.ErrPreprocess, "no package found")
	}

	return candidates, nil
}

// runDryBuild runs a dry build to get all dependencies needed for the project.
func runDryBuild(goBuildCmd []string) ([]string, error) {
	dryRunLog, err := os.Create(util.GetLogPath(DryRunLog))
	if err != nil {
		return nil, errc.New(errc.ErrCreateFile, err.Error())
	}
	// The full build command is: "go build/install -a -x -n  {...}"
	args := []string{}
	args = append(args, goBuildCmd[:2]...)             // go build/install
	args = append(args, []string{"-a", "-x", "-n"}...) // -a -x -n
	args = append(args, goBuildCmd[2:]...)             // {...} remaining
	util.AssertGoBuild(goBuildCmd)
	util.AssertGoBuild(args)

	// Run the dry build
	util.Log("Run dry build %v", args)
	cmd := exec.Command(args[0], args[1:]...)
	// This is a little anti-intuitive as the error message is not printed to
	// the stderr, instead it is printed to the stdout, only the build tool
	// knows the reason why.
	cmd.Stdout = os.Stdout
	cmd.Stderr = dryRunLog
	// @@Note that dir should not be set, as the dry build should be run in the
	// same directory as the original build command
	cmd.Dir = ""
	err = cmd.Run()
	if err != nil {
		return nil, errc.New(errc.ErrRunCmd, err.Error()).
			With("command", fmt.Sprintf("%v", args))
	}

	// Find compile commands from dry run log
	compileCmds, err := getCompileCommands()
	if err != nil {
		return nil, err
	}
	return compileCmds, nil
}

func (dp *DepProcessor) runModTidy() error {
	out, err := runCmdCombinedOutput(dp.getGoModDir(), nil,
		"go", "mod", "tidy")
	util.Log("Run go mod tidy: %v", out)
	return err
}

func (dp *DepProcessor) runModVendor() error {
	out, err := runCmdCombinedOutput(dp.getGoModDir(), nil,
		"go", "mod", "vendor")
	util.Log("Run go mod vendor: %v", out)
	return err
}

func (dp *DepProcessor) refreshDeps() error {
	// Run go mod tidy to remove unused dependencies
	err := dp.runModTidy()
	if err != nil {
		return err
	}

	// Run go mod vendor to update the vendor directory
	if dp.vendorMode {
		err = dp.runModVendor()
		if err != nil {
			return err
		}
	}

	return nil
}

func getTempGoCache() (string, error) {
	goCachePath, err := filepath.Abs(filepath.Join(util.TempBuildDir, GoCacheDir))
	if err != nil {
		return "", err
	}

	if !util.PathExists(goCachePath) {
		err = os.MkdirAll(goCachePath, 0755)
		if err != nil {
			return "", err
		}
	}
	return goCachePath, nil
}

func buildGoCacheEnv(value string) []string {
	return []string{"GOCACHE=" + value}
}

func runBuildWithToolexec(goBuildCmd []string) error {
	exe, err := os.Executable()
	if err != nil {
		return errc.New(errc.ErrGetExecutable, err.Error())
	}
	// go build/install
	args := []string{}
	args = append(args, goBuildCmd[:2]...)
	// Remix toolexec
	args = append(args, "-toolexec="+exe+" "+CompileRemix)

	// Leave the temporary compilation directory
	args = append(args, util.BuildWork)

	// Force rebuilding
	args = append(args, "-a")

	if config.GetConf().Debug {
		// Disable compiler optimizations for debugging mode
		args = append(args, "-gcflags=all=-N -l")
	}

	// Append additional build arguments provided by the user
	args = append(args, goBuildCmd[2:]...)

	util.Log("Run toolexec build: %v", args)
	util.AssertGoBuild(args)

	// get the temporary build cache path
	goCachePath, err := getTempGoCache()
	if err != nil {
		return err
	}
	util.Log("Using isolated GOCACHE: %s", goCachePath)

	// @@ Note that we should not set the working directory here, as the build
	// with toolexec should be run in the same directory as the original build
	// command
	out, err := runCmdCombinedOutput("", buildGoCacheEnv(goCachePath), args...)
	util.Log("Output from toolexec build: %v", out)
	return err
}

type Dependency struct {
	ImportPath     string // the import path of the dependency
	Version        string // the version of the dependency
	Replace        bool   // whether the dependency should be replaced
	ReplacePath    string // the path of the dependency
	ReplaceVersion string // the version of the dependency
}

func (dp *DepProcessor) addDependency(gomod string, dependencies []Dependency) error {
	modfile, err := parseGoMod(gomod)
	if err != nil {
		return err
	}
	// For each dependency, check if it is already in the go.mod file and add
	// it using require directive. If the dependency specifies a replace path,
	// then further add a replace directive if it is not already in the go.mod
	changed := false
	for _, dependency := range dependencies {
		alreadyRequire := false
		for _, r := range modfile.Require {
			if r.Mod.Path == dependency.ImportPath {
				alreadyRequire = true
				break
			}
		}
		if !alreadyRequire {
			err = modfile.AddRequire(dependency.ImportPath, dependency.Version)
			if err != nil {
				return err
			}
			changed = true
			util.Log("Add require dependency %s %s",
				dependency.ImportPath, dependency.Version)
		}
		if dependency.Replace {
			alreadyReplace := false
			for _, r := range modfile.Replace {
				if r.Old.Path == dependency.ImportPath {
					alreadyReplace = true
					break
				}
			}
			if !alreadyReplace {
				err = modfile.AddReplace(dependency.ImportPath, "",
					dependency.ReplacePath, dependency.ReplaceVersion)
				if err != nil {
					return err
				}
				changed = true
				util.Log("Add replace dependency %s %s => %s %s",
					dependency.ImportPath, dependency.Version,
					dependency.ReplacePath, dependency.ReplaceVersion)
			}
		}
	}
	// Once all dependencies are added and write back to go.mod
	if changed {
		bs, err := modfile.Format()
		if err != nil {
			return err
		}
		_, err = util.WriteFile(gomod, string(bs))
		if err != nil {
			return err
		}
	}
	return nil
}

func precheck() error {
	// Check if the project is modularized
	go11module := os.Getenv("GO111MODULE")
	if go11module == "off" {
		return errc.New(errc.ErrNotModularized, "GO111MODULE is off")
	}

	// Check if the build arguments is sane
	if len(os.Args) < 3 {
		config.PrintVersion()
		os.Exit(0)
	}
	if !strings.Contains(os.Args[1], "go") {
		config.PrintVersion()
		os.Exit(0)
	}
	if os.Args[2] != "build" && os.Args[2] != "install" {
		// exec original go command
		err := util.RunCmd(os.Args[1:]...)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	return nil
}

func (dp *DepProcessor) saveDebugFiles() {
	dir := filepath.Join(util.GetTempBuildDir(), "changed")
	err := os.MkdirAll(dir, os.ModePerm)
	if err == nil {
		for origin := range dp.backups {
			util.CopyFile(origin, filepath.Join(dir, filepath.Base(origin)))
		}
	}
	_ = util.CopyFile(dp.otelImporter, filepath.Join(dir, OtelImporter))
}

func (dp *DepProcessor) newRuleImporterWith(bundles []*resource.RuleBundle) error {
	content := "package main\n"
	builtin := map[string]string{
		// for go:linkname when declaring printstack/getstack variable
		"unsafe": "_",
		// for debug.Stack and log.Printf when declaring printstack/getstack
		// we do need import alias because user may declare global variable such
		// as "log" or "debug" in their code, which will conflict with the import
		"runtime/debug": "_otel_debug",
		// for log.Printf when declaring printstack/getstack variable
		"log": "_otel_log",
		// otel setup
		"github.com/alibaba/loongsuite-go-agent/pkg": "_",
		"go.opentelemetry.io/otel":                   "_",
		"go.opentelemetry.io/otel/sdk/trace":         "_",
		"go.opentelemetry.io/otel/baggage":           "_",
	}
	for pkg, alias := range builtin {
		content += fmt.Sprintf("import %s %q\n", alias, pkg)
	}

	// No rule bundles? We still need to generate the otel_importer.go file whose
	// purpose is to import the fundamental dependencies
	if len(bundles) == 0 {
		_, err := util.WriteFile(dp.otelImporter, content)
		if err != nil {
			return err
		}
		return nil
	}

	// Generate the otel_importer.go file with the rule bundles
	paths := map[string]bool{}
	for _, bundle := range bundles {
		for _, funcRules := range bundle.File2FuncRules {
			for _, rules := range funcRules {
				for _, rule := range rules {
					if rule.GetPath() != "" {
						paths[rule.GetPath()] = true
					}
				}
			}
		}
	}
	addDeps := make([]Dependency, 0)
	for path := range paths {
		content += fmt.Sprintf("import _ %q\n", path)
		t := strings.TrimPrefix(path, pkgPrefix)
		addDeps = append(addDeps, Dependency{
			ImportPath:     path,
			Version:        "v0.0.0-00010101000000-000000000000", // use latest version for the rule import
			Replace:        true,
			ReplacePath:    filepath.Join(dp.pkgLocalCache, t),
			ReplaceVersion: "",
		})
	}
	cnt := 0
	for _, bundle := range bundles {
		tag := ""
		// If we occasionally instrument the main package, we don't need to add
		// the linkname directive, as the target variables are already defined
		// in the main package, adding new linkname for generated code will cause
		// the symbol redefinition error.
		if bundle.ImportPath != "main" {
			tag = fmt.Sprintf("//go:linkname getstatck%d %s.OtelGetStackImpl\n",
				cnt, bundle.ImportPath)
		}
		content += tag
		s := fmt.Sprintf("var getstatck%d = _otel_debug.Stack\n", cnt)
		content += s
		if bundle.ImportPath != "main" {
			tag = fmt.Sprintf("//go:linkname printstack%d %s.OtelPrintStackImpl\n",
				cnt, bundle.ImportPath)
		}
		content += tag
		s = fmt.Sprintf("var printstack%d = func (bt []byte){ _otel_log.Printf(string(bt)) }\n", cnt)
		content += s
		cnt++
	}
	util.WriteFile(dp.otelImporter, content)

	err := dp.addDependency(dp.getGoModPath(), addDeps)
	if err != nil {
		return err
	}
	return nil
}

func Preprocess() error {
	// Make sure the project is modularized otherwise we cannot proceed
	err := precheck()
	if err != nil {
		return err
	}

	dp := newDepProcessor()

	err = dp.init()
	if err != nil {
		return err
	}
	defer func() { dp.postProcess() }()
	{
		defer util.PhaseTimer("Preprocess")()
		defer dp.saveDebugFiles()

		// Backup go.mod and add additional replace directives for the pkg module
		err = dp.rectifyMod()
		if err != nil {
			return err
		}

		// Two round of rule matching
		//    {prepare->refresh}
		//        1st match
		//    {prepare->refresh}
		//        2nd match
		//    {prepare->refresh}
		// Let's break it down a little bit. We first prepare the rule import,
		// which is used to import foundational dependencies (e.g., otel, as we
		// will instrument the otel SDK itself). Then, we perform a refresh to
		// ensure dependencies are ready and proceed to the 1st match. During
		// this phase, some rules matching specific criteria are identified. We
		// then update the rule import again to include these newly matched rules.
		// Since these rules may (and likely will) break the original dependency
		// graph, a 2nd match is required to resolve the final set of rules.
		// These final rules are used to perform a final update of the rule import.
		// At this point, all preparations are complete, and the process can
		// advance to the second stage: instrumentation.
		bundles := make([]*resource.RuleBundle, 0)
		for i := 0; i < 3; i++ {
			err = dp.newRuleImporterWith(bundles)
			if err != nil {
				return err
			}

			err = dp.refreshDeps()
			if err != nil {
				return err
			}
			if i == 2 {
				continue
			}
			bundles, err = dp.matchRules()
			if err != nil {
				return err
			}
		}

		// Rectify file rules to make sure we can find them locally
		err = dp.rectifyRule(bundles)
		if err != nil {
			return err
		}

		// From this point on, we no longer modify the rules
		err = resource.StoreRuleBundles(bundles)
		if err != nil {
			return err
		}
	}

	{
		defer util.PhaseTimer("Instrument")()

		// Run go build with toolexec to start instrumentation
		err = runBuildWithToolexec(dp.goBuildCmd)
		if err != nil {
			return err
		}
	}
	util.Log("Build completed successfully")
	return nil
}
