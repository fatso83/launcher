package wix

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-kit/kit/log/level"
	"github.com/kolide/launcher/pkg/contexts/ctxlog"
)

type wixTool struct {
	wixPath        string     // Where is wix installed
	packageRoot    string     // What's the root of the packaging files?
	buildDir       string     // The wix tools want to work in a build dir.
	msArch         string     // What's the Microsoft architecture name?
	services       []*Service // array of services.
	dockerImage    string     // If in docker, what image?
	skipValidation bool       // Skip light validation. Seems to be needed for running in 32bit wine environments.
	skipCleanup    bool       // Skip cleaning temp dirs. Useful when debugging wix generation
	cleanDirs      []string   // directories to rm on cleanup
	ui             bool       // whether or not to include a ui
	extraFiles     []extraFile

	execCC func(context.Context, string, ...string) *exec.Cmd // Allows test overrides
}

type extraFile struct {
	Name    string
	Content []byte
}

type WixOpt func(*wixTool)

func As64bit() WixOpt {
	return func(wo *wixTool) {
		wo.msArch = "x64"
	}
}

func As32bit() WixOpt {
	return func(wo *wixTool) {
		wo.msArch = "x86"
	}
}

// If you're running this in a virtual win environment, you probably
// need to skip validation. LGHT0216 is a common error.
func SkipValidation() WixOpt {
	return func(wo *wixTool) {
		wo.skipValidation = true
	}
}

func WithWix(path string) WixOpt {
	return func(wo *wixTool) {
		wo.wixPath = path
	}
}

func WithService(service *Service) WixOpt {
	return func(wo *wixTool) {
		wo.services = append(wo.services, service)
	}
}

func WithBuildDir(path string) WixOpt {
	return func(wo *wixTool) {
		wo.buildDir = path
	}
}

func WithDocker(image string) WixOpt {
	return func(wo *wixTool) {
		wo.dockerImage = image
	}
}

func WithUI() WixOpt {
	return func(wo *wixTool) {
		wo.ui = true
	}
}

func WithFile(name string, content []byte) WixOpt {
	return func(wo *wixTool) {
		wo.extraFiles = append(wo.extraFiles, extraFile{name, content})
	}
}

func SkipCleanup() WixOpt {
	return func(wo *wixTool) {
		wo.skipCleanup = true
	}
}

// New takes a packageRoot of files, and a wxsContent of xml wix
// configuration, and will return a struct with methods for building
// packages with.
func New(packageRoot string, mainWxsContent []byte, wixOpts ...WixOpt) (*wixTool, error) {
	wo := &wixTool{
		wixPath:     FindWixInstall(),
		packageRoot: packageRoot,

		execCC: exec.CommandContext,
	}

	for _, opt := range wixOpts {
		opt(wo)
	}

	var err error
	if wo.buildDir == "" {
		wo.buildDir, err = os.MkdirTemp("", "wix-build-dir")
		if err != nil {
			return nil, fmt.Errorf("making temp wix-build-dir: %w", err)
		}
		wo.cleanDirs = append(wo.cleanDirs, wo.buildDir)
	}

	if wo.msArch == "" {
		switch runtime.GOARCH {
		case "386":
			wo.msArch = "x86"
		case "amd64":
			wo.msArch = "x64"
		default:
			return nil, fmt.Errorf("unknown arch for windows %s", runtime.GOARCH)
		}
	}

	for _, ef := range wo.extraFiles {
		if err := os.WriteFile(
			filepath.Join(wo.buildDir, ef.Name),
			ef.Content,
			0644); err != nil {
			return nil, fmt.Errorf("writing extra file %s: %w", ef.Name, err)
		}
	}

	mainWxsPath := filepath.Join(wo.buildDir, "Installer.wxs")

	if err := os.WriteFile(
		mainWxsPath,
		mainWxsContent,
		0644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", mainWxsPath, err)
	}

	return wo, nil
}

// Cleanup removes temp directories. Meant to be called in a defer.
func (wo *wixTool) Cleanup() {
	if wo.skipCleanup {
		return
	}

	for _, d := range wo.cleanDirs {
		os.RemoveAll(d)
	}
}

// Package will run through the wix steps to produce a resulting
// package. The path for the resultant package will be returned.
func (wo *wixTool) Package(ctx context.Context) (string, error) {
	if err := wo.heat(ctx); err != nil {
		return "", fmt.Errorf("running heat: %w", err)
	}

	if err := wo.addServices(ctx); err != nil {
		return "", fmt.Errorf("adding services: %w", err)
	}

	if err := wo.candle(ctx); err != nil {
		return "", fmt.Errorf("running candle: %w", err)
	}

	if err := wo.light(ctx); err != nil {
		return "", fmt.Errorf("running light: %w", err)
	}

	return filepath.Join(wo.buildDir, "out.msi"), nil
}

// addServices adds service definitions into the wix configs.
//
// In wix parlence, these schema elements are _in_ the Component
// section, which is autogenerated by heat.exe. This presents a
// problem -- How do we modify that? We could manually curate the
// files list, we could pass heat an xslt transform, or we can
// post-process the wxs files. I've opted to post-process them.
//
// References:
//   - http://windows-installer-xml-wix-toolset.687559.n2.nabble.com/Windows-Service-installation-td7601050.html
//   - https://helgeklein.com/blog/2014/09/real-world-example-wix-msi-application-installer/
func (wo *wixTool) addServices(ctx context.Context) error {
	if len(wo.services) == 0 {
		return nil
	}

	heatFile := filepath.Join(wo.buildDir, "AppFiles.wxs")
	heatContent, err := os.ReadFile(heatFile)
	if err != nil {
		return fmt.Errorf("reading AppFiles.wxs: %w", err)
	}

	heatWrite, err := os.Create(heatFile)
	if err != nil {
		return fmt.Errorf("opening AppFiles.wxs for writing: %w", err)
	}
	defer heatWrite.Close()

	lines := strings.Split(string(heatContent), "\n")
	for _, line := range lines {
		heatWrite.WriteString(line)
		heatWrite.WriteString("\n")
		for _, service := range wo.services {
			isMatch, err := service.Match(line)
			if err != nil {
				return fmt.Errorf("match error: %w", err)
			}
			if isMatch {
				if err := service.Xml(heatWrite); err != nil {
					return fmt.Errorf("adding service: %w", err)
				}
			}
		}
	}

	return nil
}

// heat invokes wix's heat command. This examines a directory and
// "harvests" the files into an xml structure. See
// http://wixtoolset.org/documentation/manual/v3/overview/heat.html
//
// TODO split this into PROGDIR and DATADIR. Perhaps using options? Or
// figuring out a way to invoke this multiple times with different dir
// and -cg settings. Historically this used PROGDIR, and I haven't dug
// into the auto-update code, so it's staying there for now.
func (wo *wixTool) heat(ctx context.Context) error {
	_, err := wo.execOut(ctx,
		filepath.Join(wo.wixPath, "heat.exe"),
		"dir", wo.packageRoot,
		"-nologo",
		"-gg", "-g1",
		"-srd",
		"-sfrag",
		"-ke",
		"-cg", "AppFiles",
		"-template", "fragment",
		"-dr", "PROGDIR",
		"-var", "var.SourceDir",
		"-out", "AppFiles.wxs",
	)
	return err
}

// candle invokes wix's candle command. This is the wix compiler, It
// preprocesses and compiles WiX source files into object files
// (.wixobj).
func (wo *wixTool) candle(ctx context.Context) error {
	_, err := wo.execOut(ctx,
		filepath.Join(wo.wixPath, "candle.exe"),
		"-nologo",
		"-arch", wo.msArch,
		"-dSourceDir="+wo.packageRoot,
		"-ext", "WixUtilExtension",
		"Installer.wxs",
		"AppFiles.wxs",
	)
	return err
}

// light invokes wix's light command. This links and binds one or more
// .wixobj files and creates a Windows Installer database (.msi or
// .msm). See http://wixtoolset.org/documentation/manual/v3/overview/light.html for options
func (wo *wixTool) light(ctx context.Context) error {
	args := []string{
		"-nologo",
		"-dcl:high", // compression level
		"-dSourceDir=" + wo.packageRoot,
		"-ext", "WixUtilExtension",
		"AppFiles.wixobj",
		"Installer.wixobj",
		"-out", "out.msi",
	}

	if wo.ui {
		args = append(args, "-ext", "WixUIExtension")
	}

	if wo.skipValidation {
		args = append(args, "-sval")
	}

	_, err := wo.execOut(ctx,
		filepath.Join(wo.wixPath, "light.exe"),
		args...,
	)
	return err

}

func (wo *wixTool) execOut(ctx context.Context, argv0 string, args ...string) (string, error) {
	logger := ctxlog.FromContext(ctx)

	dockerArgs := []string{
		"run",
		"--entrypoint", "",
		"-v", fmt.Sprintf("%s:%s", wo.packageRoot, wo.packageRoot),
		"-v", fmt.Sprintf("%s:%s", wo.buildDir, wo.buildDir),
		"-w", wo.buildDir,
		wo.dockerImage,
		"wine",
		argv0,
	}

	dockerArgs = append(dockerArgs, args...)

	if wo.dockerImage != "" {
		argv0 = "docker"
		args = dockerArgs
	}

	cmd := wo.execCC(ctx, argv0, args...)

	level.Debug(logger).Log(
		"msg", "execing",
		"cmd", strings.Join(cmd.Args, " "),
	)

	cmd.Dir = wo.buildDir
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run command %s %v\nstdout=%s\nstderr=%s: %w", argv0, args, stdout, stderr, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// FindWixInstall will return the path wix will be executed from. This
// is exposed here, and not an internal method, as a convinience to
// `package-builder`
func FindWixInstall() string {
	// wix exe installers set an env
	if p := os.Getenv("WIX"); p != "" {
		return p + `\bin`
	}

	for _, p := range []string{`C:\wix311`} {
		if isDirectory(p) == nil {
			return p
		}
	}

	return ""
}

func isDirectory(d string) error {
	dStat, err := os.Stat(d)

	if os.IsNotExist(err) {
		return fmt.Errorf("missing packageRoot %s: %w", d, err)
	}

	if !dStat.IsDir() {
		return fmt.Errorf("packageRoot (%s) isn't a directory", d)
	}

	return nil
}
