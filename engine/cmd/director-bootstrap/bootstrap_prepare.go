// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var httpClient = &http.Client{
	Timeout: 5 * time.Minute,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return fail("DIRECTOR_BOOTSTRAP_RELEASE_FETCH")
		}
		return validateAssetURL(request.URL.String())
	},
}

type commandResult struct {
	stdout string
	stderr string
}

var runCommand = func(ctx context.Context, executable string, args []string, cwd string, environment map[string]string) (commandResult, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = cwd
	command.Env = sortedEnvironment(environment)
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maximumCommandOutput}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: maximumCommandOutput}
	if err := command.Run(); err != nil {
		return commandResult{stdout: stdout.String(), stderr: stderr.String()}, err
	}
	return commandResult{stdout: strings.TrimSpace(stdout.String()), stderr: strings.TrimSpace(stderr.String())}, nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

type executableOwnership struct {
	uid  uint32
	mode os.FileMode
}

type goToolchainSelector func(home string) (string, error)

func (writer *limitedWriter) Write(value []byte) (int, error) {
	if len(value) > writer.remaining {
		return 0, fail("DIRECTOR_BOOTSTRAP_OUTPUT_LIMIT")
	}
	written, err := writer.writer.Write(value)
	writer.remaining -= written
	return written, err
}

func validateAssetURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" ||
		(parsed.Hostname() != "github.com" && parsed.Hostname() != "release-assets.githubusercontent.com") {
		return fail("DIRECTOR_BOOTSTRAP_RELEASE_URL")
	}
	return nil
}

func exactDirectorAsset(asset releaseAsset, version, name string) bool {
	return asset.Name == name && asset.URL == "https://github.com/mcuadros/paseo-director/releases/download/v"+version+"/"+name
}

func downloadAsset(asset releaseAsset, maximum int64) ([]byte, error) {
	if asset.Name == "" || len(asset.Name) > 128 || strings.ContainsAny(asset.Name, `/\\`) || !sha256Pattern.MatchString(asset.SHA256) || asset.Size <= 0 || asset.Size > maximum {
		return nil, fail("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST")
	}
	if err := validateAssetURL(asset.URL); err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, fail("DIRECTOR_BOOTSTRAP_RELEASE_FETCH")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fail("DIRECTOR_BOOTSTRAP_RELEASE_FETCH")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maximum {
		return nil, fail("DIRECTOR_BOOTSTRAP_RELEASE_FETCH")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || int64(len(content)) != asset.Size || int64(len(content)) > maximum || digestBytes(content) != asset.SHA256 {
		return nil, fail("DIRECTOR_BOOTSTRAP_RELEASE_DIGEST")
	}
	return content, nil
}

func trustedSystemExecutable(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	var parents []executableOwnership
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		for _, directory := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
			parent, parentErr := os.Lstat(directory)
			if parentErr != nil {
				return false
			}
			parentStat, parentOK := parent.Sys().(*syscall.Stat_t)
			if !parentOK {
				return false
			}
			parents = append(parents, executableOwnership{uid: parentStat.Uid, mode: parent.Mode()})
		}
	}
	if !trustedExecutableOwner(stat.Uid, os.Geteuid(), parents) {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != filepath.Clean(path) {
		return false
	}
	content, err := os.Open(path)
	if err != nil {
		return false
	}
	defer content.Close()
	header := make([]byte, 4)
	if _, err := io.ReadFull(content, header); err != nil {
		return false
	}
	return bytes.Equal(header, []byte{0x7f, 'E', 'L', 'F'})
}

func trustedExecutableOwner(executableUID uint32, effectiveUID int, parents []executableOwnership) bool {
	if executableUID == 0 || int(executableUID) == effectiveUID {
		return true
	}
	if len(parents) != 2 {
		return false
	}
	for _, parent := range parents {
		if parent.uid != executableUID || !parent.mode.IsDir() || parent.mode&os.ModeSymlink != 0 || parent.mode.Perm()&0o022 != 0 {
			return false
		}
	}
	return true
}

func productionGoToolchainCandidates() []string {
	return []string{"/usr/local/go/bin/go", "/usr/bin/go"}
}

func exactGoToolchain(home string) (string, error) {
	return exactGoToolchainFromCandidates(home, productionGoToolchainCandidates())
}

func exactGoToolchainFromCandidates(home string, candidates []string) (string, error) {
	versionMismatch := false
	capabilityMismatch := false
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) || !trustedSystemExecutable(candidate) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		version, err := runCommand(ctx, candidate, []string{"version"}, filepath.Dir(filepath.Dir(candidate)), map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"})
		cancel()
		if err != nil || !regexp.MustCompile(`\b`+regexp.QuoteMeta(requiredGoVersion)+`\b`).MatchString(version.stdout) {
			versionMismatch = true
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		capability, err := runCommand(ctx, candidate, []string{"env", "GOOS", "GOARCH", "GOVERSION"}, filepath.Dir(filepath.Dir(candidate)), map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"})
		cancel()
		if err == nil && capability.stdout == "linux\namd64\n"+requiredGoVersion {
			return candidate, nil
		}
		capabilityMismatch = true
	}
	if capabilityMismatch {
		return "", fail("DIRECTOR_MAIN_GO_TOOLCHAIN_CAPABILITY")
	}
	if versionMismatch {
		return "", fail("DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION")
	}
	return "", fail("DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING")
}

func exactGitCheckout(root, candidate, home string) error {
	git := "/usr/bin/git"
	if !trustedSystemExecutable(git) {
		return fail("DIRECTOR_MAIN_GIT_MISSING")
	}
	environment := map[string]string{"HOME": home, "GIT_CONFIG_NOSYSTEM": "1", "PATH": "/usr/bin"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	head, err := runCommand(ctx, git, []string{"-C", root, "rev-parse", "HEAD"}, root, environment)
	cancel()
	if err != nil || head.stdout != candidate {
		return fail("DIRECTOR_MAIN_SOURCE_IDENTITY")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	status, err := runCommand(ctx, git, []string{"-C", root, "status", "--porcelain=v1", "--untracked-files=no"}, root, environment)
	cancel()
	generatedOnly := regexp.MustCompile(`^(?: M|M |MM) connector/install-metadata\.server\.ts$`).MatchString(status.stdout)
	if err != nil || (status.stdout != "" && !generatedOnly) {
		return fail("DIRECTOR_MAIN_SOURCE_IDENTITY")
	}
	return nil
}

func publishExecutable(path string, content []byte, mode os.FileMode) error {
	if _, err := os.Lstat(path); err == nil {
		digest, size, verifyErr := digestFile(path, int64(len(content))+1)
		if verifyErr != nil || digest != digestBytes(content) || size != int64(len(content)) {
			return fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED")
		}
		return nil
	}
	return writePrivateAtomic(path, content, mode)
}

func engineIdentity(path string) (preparedEngine, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runCommand(ctx, path, []string{"version"}, filepath.Dir(path), map[string]string{})
	if err != nil {
		return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_ENGINE_IDENTITY")
	}
	var identity struct {
		Name, Version, BuildMode, SourceCandidate, Target, ExecutableSHA256, NoticesSHA256, ContractVersion, ContractSHA256 string
		ProductBehavior                                                                                                     bool
	}
	if err := json.Unmarshal([]byte(result.stdout), &identity); err != nil || identity.Name != "director-engine" || !identity.ProductBehavior ||
		!sha256Pattern.MatchString(identity.ExecutableSHA256) || !sha256Pattern.MatchString(identity.NoticesSHA256) || !sha256Pattern.MatchString(identity.ContractSHA256) {
		return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_ENGINE_IDENTITY")
	}
	return preparedEngine{Mode: identity.BuildMode, Version: identity.Version, SourceCandidate: identity.SourceCandidate, Target: identity.Target,
		Binary:  preparedExecutable{Path: path, SHA256: identity.ExecutableSHA256},
		Notices: preparedExecutable{SHA256: identity.NoticesSHA256}, ContractVersion: identity.ContractVersion, ContractSHA256: identity.ContractSHA256}, nil
}

func validateDoltPin(pin doltPin) error {
	if (pin.SchemaVersion != 0 && pin.SchemaVersion != 1) || (pin.Target != "" && pin.Target != target) ||
		pin.Version != "2.3.2" || pin.Archive.Name != "dolt-linux-amd64.tar.gz" ||
		pin.Archive.URL != "https://github.com/dolthub/dolt/releases/download/v2.3.2/dolt-linux-amd64.tar.gz" ||
		!sha256Pattern.MatchString(pin.Archive.SHA256) || pin.Archive.Size <= 0 || pin.Archive.Size > maximumDoltArchive ||
		!sha256Pattern.MatchString(pin.ExecutableSHA256) || pin.ExecutableSize <= 0 || pin.ExecutableSize > maximumEngineBytes {
		return fail("DIRECTOR_BOOTSTRAP_DOLT_MANIFEST")
	}
	return nil
}

func extractDolt(archive []byte, expectedSize int64) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(io.LimitReader(gzipReader, maximumDoltArchive+1))
	var executable []byte
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
		}
		clean := filepath.ToSlash(filepath.Clean(header.Name))
		if header.Name == "" || len(header.Name) > 512 || filepath.IsAbs(header.Name) || strings.Contains(header.Name, "\\") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
		}
		if (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) && strings.HasSuffix(clean, "/bin/dolt") {
			if executable != nil || header.Size != expectedSize {
				return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
			}
			executable, err = io.ReadAll(io.LimitReader(reader, expectedSize+1))
			if err != nil || int64(len(executable)) != expectedSize {
				return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
			}
		}
	}
	if executable == nil {
		return nil, fail("DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE")
	}
	return executable, nil
}

func verifyDoltBinary(path, version string) error {
	home := filepath.Join(filepath.Dir(path), ".identity-home")
	if err := ensurePrivateDir(filepath.Join(home, ".dolt")); err != nil {
		return err
	}
	config := filepath.Join(home, ".dolt", "config_global.json")
	if _, err := os.Lstat(config); os.IsNotExist(err) {
		if err := writePrivateAtomic(config, []byte("{\"versioncheck.disabled\":\"true\"}\n"), 0o600); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runCommand(ctx, path, []string{"version"}, filepath.Dir(path), map[string]string{"HOME": home, "DOLT_DISABLE_VERSION_CHECK": "1"})
	if err != nil || !strings.HasPrefix(result.stdout, "dolt version "+version) {
		return fail("DIRECTOR_BOOTSTRAP_DOLT_IDENTITY")
	}
	return nil
}

func prepareDolt(paths runtimePaths, pin doltPin) (preparedDolt, error) {
	if err := validateDoltPin(pin); err != nil {
		return preparedDolt{}, err
	}
	root := filepath.Join(paths.cacheRoot, "engines", "dolt", pin.Version, target, pin.ExecutableSHA256)
	binary := filepath.Join(root, "dolt")
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		archive, err := downloadAsset(pin.Archive, maximumDoltArchive)
		if err != nil {
			return preparedDolt{}, err
		}
		content, err := extractDolt(archive, pin.ExecutableSize)
		if err != nil {
			return preparedDolt{}, err
		}
		if digestBytes(content) != pin.ExecutableSHA256 {
			return preparedDolt{}, fail("DIRECTOR_BOOTSTRAP_DOLT_DIGEST")
		}
		parent := filepath.Dir(root)
		if err := ensurePrivateDir(parent); err != nil {
			return preparedDolt{}, err
		}
		temporary := filepath.Join(parent, ".dolt-partial-"+randomHex(8))
		if err := ensurePrivateDir(temporary); err != nil {
			return preparedDolt{}, err
		}
		defer os.RemoveAll(temporary)
		if err := publishExecutable(filepath.Join(temporary, "dolt"), content, 0o500); err != nil {
			return preparedDolt{}, err
		}
		if err := verifyDoltBinary(filepath.Join(temporary, "dolt"), pin.Version); err != nil {
			return preparedDolt{}, err
		}
		if err := fsyncDir(temporary); err != nil {
			return preparedDolt{}, fail("DIRECTOR_BOOTSTRAP_DOLT_CACHE_IO")
		}
		if err := os.Rename(temporary, root); err != nil {
			return preparedDolt{}, fail("DIRECTOR_BOOTSTRAP_DOLT_CACHE_IO")
		}
		if err := fsyncDir(parent); err != nil {
			return preparedDolt{}, fail("DIRECTOR_BOOTSTRAP_DOLT_CACHE_IO")
		}
	}
	digest, size, err := digestFile(binary, maximumEngineBytes)
	if err != nil || digest != pin.ExecutableSHA256 || size != pin.ExecutableSize {
		return preparedDolt{}, fail("DIRECTOR_BOOTSTRAP_DOLT_CACHE_POISONED")
	}
	if err := verifyDoltBinary(binary, pin.Version); err != nil {
		return preparedDolt{}, err
	}
	return preparedDolt{Version: pin.Version, Target: target, Binary: preparedExecutable{Path: binary, SHA256: digest, Size: size}, ArchiveSHA256: pin.Archive.SHA256}, nil
}

func buildMainEngine(root, candidate string, paths runtimePaths) (preparedEngine, int, error) {
	return buildMainEngineWithToolchain(root, candidate, paths, exactGoToolchain)
}

func buildMainEngineWithToolchain(root, candidate string, paths runtimePaths, selectGo goToolchainSelector) (preparedEngine, int, error) {
	home, _ := os.UserHomeDir()
	if err := exactGitCheckout(root, candidate, home); err != nil {
		return preparedEngine{}, 0, err
	}
	goTool, err := selectGo(home)
	if err != nil {
		return preparedEngine{}, 0, err
	}
	generated, err := os.ReadFile(filepath.Join(root, "generated", "host-contract.shared.ts"))
	if err != nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_SOURCE_CONTRACT")
	}
	versionMatch := regexp.MustCompile(`HOST_CONTRACT_VERSION = "([^"]+)"`).FindSubmatch(generated)
	digestMatch := regexp.MustCompile(`HOST_CONTRACT_SHA256 = "([0-9a-f]{64})"`).FindSubmatch(generated)
	if len(versionMatch) != 2 || len(digestMatch) != 2 {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_SOURCE_CONTRACT")
	}
	contractDigest := string(digestMatch[1])
	base := filepath.Join(paths.cacheRoot, "engines", "main", candidate, target, contractDigest)
	preparedFile := filepath.Join(base, "prepared.json")
	if _, err := os.Lstat(preparedFile); err == nil {
		var existing preparedEngine
		if err := decodeStrict(preparedFile, maximumManifestBytes, &existing); err != nil {
			return preparedEngine{}, 0, fail("DIRECTOR_MAIN_CACHE_POISONED")
		}
		if err := verifyPreparedEngine(existing, "main", candidate); err != nil {
			return preparedEngine{}, 0, fail("DIRECTOR_MAIN_CACHE_POISONED")
		}
		return existing, 0, nil
	}
	if err := ensurePrivateDir(base); err != nil {
		return preparedEngine{}, 0, err
	}
	noticesBytes := []byte(fmt.Sprintf("Director Engine exact main source-testing build\nsource-candidate: %s\ncontract-sha256: %s\nmodule-policy: go.sum; -mod=readonly; GOPROXY=off\n", candidate, contractDigest))
	noticesDigest := digestBytes(noticesBytes)
	temporary := filepath.Join(base, ".engine-partial-"+randomHex(8))
	if err := ensurePrivateDir(temporary); err != nil {
		return preparedEngine{}, 0, err
	}
	defer os.RemoveAll(temporary)
	binaryTemp := filepath.Join(temporary, "director-engine")
	goCache := filepath.Join(paths.cacheRoot, "go-build-cache", requiredGoVersion)
	if err := ensurePrivateDir(goCache); err != nil {
		return preparedEngine{}, 0, err
	}
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		moduleCache = filepath.Join(home, "go", "pkg", "mod")
	}
	if !filepath.IsAbs(moduleCache) {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_MODULE_CACHE")
	}
	environment := map[string]string{"HOME": home, "GOCACHE": goCache, "GOMODCACHE": moduleCache, "CGO_ENABLED": "0", "GOARCH": "amd64", "GOOS": "linux", "GOENV": "off", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	_, commandErr := runCommand(ctx, goTool, []string{"build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-ldflags", fmt.Sprintf("-s -w -X main.buildMode=main -X main.version=%s -X main.sourceCandidate=%s -X main.noticesSha=%s", mainEngineVersion, candidate, noticesDigest), "-o", binaryTemp, "./cmd/director-engine"}, filepath.Join(root, "engine"), environment)
	cancel()
	if commandErr != nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_BUILD_FAILED")
	}
	if err := os.Chmod(binaryTemp, 0o500); err != nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_BUILD_FAILED")
	}
	identity, err := engineIdentity(binaryTemp)
	if err != nil {
		return preparedEngine{}, 0, err
	}
	binaryDigest, binarySize, err := digestFile(binaryTemp, maximumEngineBytes)
	if err != nil {
		return preparedEngine{}, 0, err
	}
	if identity.Mode != "main" || identity.Version != mainEngineVersion || identity.SourceCandidate != candidate || identity.Target != target || identity.Binary.SHA256 != binaryDigest || identity.Notices.SHA256 != noticesDigest || identity.ContractVersion != string(versionMatch[1]) || identity.ContractSHA256 != contractDigest {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_IDENTITY_MISMATCH")
	}
	closure := filepath.Join(base, binaryDigest)
	if _, err := os.Lstat(closure); err == nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_CACHE_POISONED")
	}
	closureStage := filepath.Join(base, ".closure-partial-"+randomHex(8))
	if err := ensurePrivateDir(closureStage); err != nil {
		return preparedEngine{}, 0, err
	}
	defer os.RemoveAll(closureStage)
	binary := filepath.Join(closure, "director-engine")
	notices := filepath.Join(closure, "MAIN_BUILD_NOTICES.txt")
	binaryBytes, _ := os.ReadFile(binaryTemp)
	if err := publishExecutable(filepath.Join(closureStage, "director-engine"), binaryBytes, 0o500); err != nil {
		return preparedEngine{}, 0, err
	}
	if err := publishExecutable(filepath.Join(closureStage, "MAIN_BUILD_NOTICES.txt"), noticesBytes, 0o400); err != nil {
		return preparedEngine{}, 0, err
	}
	if err := fsyncDir(closureStage); err != nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_CACHE_IO")
	}
	if err := os.Rename(closureStage, closure); err != nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_CACHE_IO")
	}
	if err := fsyncDir(base); err != nil {
		return preparedEngine{}, 0, fail("DIRECTOR_MAIN_CACHE_IO")
	}
	identity.Binary = preparedExecutable{Path: binary, SHA256: binaryDigest, Size: binarySize}
	identity.Notices = preparedExecutable{Path: notices, SHA256: noticesDigest, Size: int64(len(noticesBytes))}
	content, _ := json.Marshal(identity)
	if err := writePrivateAtomic(preparedFile, append(content, '\n'), 0o400); err != nil {
		return preparedEngine{}, 0, err
	}
	return identity, 1, nil
}

func verifyPreparedEngine(engine preparedEngine, mode, candidate string) error {
	if engine.Mode != mode || engine.SourceCandidate != candidate || engine.Target != target || !sha256Pattern.MatchString(engine.ContractSHA256) || engine.ContractVersion == "" {
		return fail("DIRECTOR_BOOTSTRAP_ENGINE_IDENTITY")
	}
	for _, item := range []preparedExecutable{engine.Binary, engine.Notices} {
		digest, size, err := digestFile(item.Path, maximumEngineBytes)
		if err != nil || digest != item.SHA256 || size != item.Size {
			return fail("DIRECTOR_BOOTSTRAP_ENGINE_CACHE_POISONED")
		}
	}
	identity, err := engineIdentity(engine.Binary.Path)
	if err != nil || identity.Mode != mode || identity.Version != engine.Version || identity.SourceCandidate != candidate || identity.Target != target || identity.Binary.SHA256 != engine.Binary.SHA256 || identity.Notices.SHA256 != engine.Notices.SHA256 || identity.ContractVersion != engine.ContractVersion || identity.ContractSHA256 != engine.ContractSHA256 {
		return fail("DIRECTOR_BOOTSTRAP_ENGINE_IDENTITY")
	}
	return nil
}

func prepareReleaseEngine(paths runtimePaths, manifest engineReleaseManifest) (preparedEngine, error) {
	if manifest.SchemaVersion != 2 || manifest.State != "published" || manifest.Target != target || !gitSHAPattern.MatchString(manifest.SourceCandidate) || manifest.Dolt == nil {
		return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST")
	}
	if !regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`).MatchString(manifest.Version) ||
		!exactDirectorAsset(manifest.Binary, manifest.Version, "director-engine-linux-amd64") ||
		!exactDirectorAsset(manifest.Notices, manifest.Version, "THIRD_PARTY_NOTICES.txt") {
		return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST")
	}
	root := filepath.Join(paths.cacheRoot, "engines", "releases", manifest.Version, target, manifest.Binary.SHA256)
	binary := filepath.Join(root, manifest.Binary.Name)
	notices := filepath.Join(root, manifest.Notices.Name)
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		binaryBytes, err := downloadAsset(manifest.Binary, maximumEngineBytes)
		if err != nil {
			return preparedEngine{}, err
		}
		noticesBytes, err := downloadAsset(manifest.Notices, maximumNoticesBytes)
		if err != nil {
			return preparedEngine{}, err
		}
		if !strings.Contains(string(noticesBytes), "source-candidate: "+manifest.SourceCandidate+"\n") {
			return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_NOTICES")
		}
		parent := filepath.Dir(root)
		if err := ensurePrivateDir(parent); err != nil {
			return preparedEngine{}, err
		}
		temporary := filepath.Join(parent, ".engine-partial-"+randomHex(8))
		if err := ensurePrivateDir(temporary); err != nil {
			return preparedEngine{}, err
		}
		defer os.RemoveAll(temporary)
		if err := publishExecutable(filepath.Join(temporary, manifest.Binary.Name), binaryBytes, 0o500); err != nil {
			return preparedEngine{}, err
		}
		if err := publishExecutable(filepath.Join(temporary, manifest.Notices.Name), noticesBytes, 0o400); err != nil {
			return preparedEngine{}, err
		}
		if err := fsyncDir(temporary); err != nil {
			return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_CACHE_IO")
		}
		if err := os.Rename(temporary, root); err != nil {
			return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_CACHE_IO")
		}
		if err := fsyncDir(parent); err != nil {
			return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_CACHE_IO")
		}
	}
	identity, err := engineIdentity(binary)
	if err != nil {
		return preparedEngine{}, err
	}
	identity.Binary.Path = binary
	identity.Notices.Path = notices
	identity.Binary.Size = manifest.Binary.Size
	identity.Notices.Size = manifest.Notices.Size
	if identity.Mode != "release" || identity.Version != manifest.Version || identity.SourceCandidate != manifest.SourceCandidate || identity.Target != target || identity.Binary.SHA256 != manifest.Binary.SHA256 || identity.Notices.SHA256 != manifest.Notices.SHA256 {
		return preparedEngine{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_IDENTITY")
	}
	if err := verifyPreparedEngine(identity, "release", manifest.SourceCandidate); err != nil {
		return preparedEngine{}, err
	}
	return identity, nil
}

func selfExecutable(expectedSHA string) (preparedExecutable, error) {
	path, err := os.Executable()
	if err != nil {
		return preparedExecutable{}, fail("DIRECTOR_BOOTSTRAP_SELF_IDENTITY")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		return preparedExecutable{}, fail("DIRECTOR_BOOTSTRAP_SELF_IDENTITY")
	}
	digest, size, err := digestFile(path, maximumEngineBytes)
	if err != nil || digest != expectedSHA {
		return preparedExecutable{}, fail("DIRECTOR_BOOTSTRAP_SELF_IDENTITY")
	}
	return preparedExecutable{Path: path, SHA256: digest, Size: size}, nil
}

func prepare(candidate, bootstrapSHA string) (prepareResult, error) {
	if !platformOK() {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_PLATFORM_UNSUPPORTED")
	}
	if !gitSHAPattern.MatchString(candidate) || !sha256Pattern.MatchString(bootstrapSHA) {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
	}
	root, err := os.Getwd()
	if err != nil {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_SOURCE_IDENTITY")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_SOURCE_IDENTITY")
	}
	paths, err := runtimeBasePaths()
	if err != nil {
		return prepareResult{}, err
	}
	for _, path := range []string{paths.cacheRoot, filepath.Join(paths.cacheRoot, "runtime")} {
		if err := ensurePrivateDir(path); err != nil {
			return prepareResult{}, err
		}
	}
	preparationRoot := filepath.Dir(preparedPath(paths, candidate))
	if err := ensurePrivateDir(preparationRoot); err != nil {
		return prepareResult{}, err
	}
	lock, err := acquireRuntimeLock(filepath.Join(preparationRoot, "preparation.lock"))
	if err != nil {
		return prepareResult{}, err
	}
	defer releaseRuntimeLock(lock)
	self, err := selfExecutable(bootstrapSHA)
	if err != nil {
		return prepareResult{}, err
	}
	var engineManifest engineReleaseManifest
	if err := decodeStrict(filepath.Join(root, "release", "engine.json"), maximumManifestBytes, &engineManifest); err != nil {
		return prepareResult{}, err
	}
	var doltManifest doltPin
	if err := decodeStrict(filepath.Join(root, "release", "dolt-linux-amd64.json"), maximumManifestBytes, &doltManifest); err != nil {
		return prepareResult{}, err
	}
	if err := validateDoltPin(doltManifest); err != nil {
		return prepareResult{}, err
	}
	var channel string
	var engine preparedEngine
	engineBuilds := 0
	if engineManifest.State == "unpublished" {
		if engineManifest.SchemaVersion != 2 || engineManifest.Version != "0.0.0-scaffold" || engineManifest.Target != target ||
			engineManifest.SourceCandidate != "" || engineManifest.Binary != (releaseAsset{}) || engineManifest.Notices != (releaseAsset{}) || engineManifest.Dolt != nil ||
			bootstrapMode != "main" || bootstrapCandidate != candidate {
			return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_MAIN_MANIFEST")
		}
		channel = "main"
		engine, engineBuilds, err = buildMainEngine(root, candidate, paths)
	} else if engineManifest.State == "published" {
		if engineManifest.SourceCandidate != candidate || bootstrapMode != "release" || bootstrapCandidate != candidate {
			return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST")
		}
		var bootstrapManifest bootstrapReleaseManifest
		if decodeErr := decodeStrict(filepath.Join(root, "release", "bootstrap-linux-amd64.json"), maximumManifestBytes, &bootstrapManifest); decodeErr != nil {
			return prepareResult{}, decodeErr
		}
		if bootstrapManifest.SchemaVersion != 1 || bootstrapManifest.State != "published" || bootstrapManifest.Version != engineManifest.Version || bootstrapManifest.Target != target || bootstrapManifest.SourceCandidate != candidate ||
			!exactDirectorAsset(bootstrapManifest.Binary, bootstrapManifest.Version, "director-bootstrap-linux-amd64") || bootstrapManifest.Binary.SHA256 != bootstrapSHA || bootstrapManifest.Binary.Size != self.Size {
			return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST")
		}
		channel = "release"
		engine, err = prepareReleaseEngine(paths, engineManifest)
	} else {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_CHANNEL_INVALID")
	}
	if err != nil {
		return prepareResult{}, err
	}
	dolt, err := prepareDolt(paths, doltManifest)
	if err != nil {
		return prepareResult{}, err
	}
	if engineManifest.State == "published" && (engineManifest.Dolt == nil ||
		engineManifest.Dolt.Version != doltManifest.Version ||
		canonicalJSONDigest(engineManifest.Dolt.Archive) != canonicalJSONDigest(doltManifest.Archive) ||
		engineManifest.Dolt.ExecutableSHA256 != doltManifest.ExecutableSHA256 ||
		engineManifest.Dolt.ExecutableSize != doltManifest.ExecutableSize) {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_DOLT_MANIFEST")
	}
	prepared := preparedRuntime{SchemaVersion: 1, Channel: channel, SourceCandidate: candidate, Target: target, Bootstrap: self, Engine: engine, Dolt: dolt}
	path := preparedPath(paths, candidate)
	content, _ := json.Marshal(prepared)
	if existing, readErr := os.ReadFile(path); readErr == nil && !bytes.Equal(existing, append(content, '\n')) {
		return prepareResult{}, fail("DIRECTOR_BOOTSTRAP_PREPARED_POISONED")
	}
	if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
		if err := writePrivateAtomic(path, append(content, '\n'), 0o400); err != nil {
			return prepareResult{}, err
		}
	}
	if _, err := loadPrepared(paths, candidate, bootstrapSHA); err != nil {
		return prepareResult{}, err
	}
	return prepareResult{SchemaVersion: 1, Code: "DIRECTOR_BOOTSTRAP_PREPARED", Channel: channel, SourceCandidate: candidate, Bootstrap: self, Engine: engine, Dolt: dolt, EngineBuilds: engineBuilds}, nil
}

func scanLine(reader io.Reader, maximum int) (string, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, int64(maximum)+1))
	scanner.Buffer(make([]byte, 1024), maximum)
	if !scanner.Scan() {
		return "", scanner.Err()
	}
	if scanner.Scan() {
		return "", fail("DIRECTOR_BOOTSTRAP_CONTROL_INVALID")
	}
	return scanner.Text(), nil
}
