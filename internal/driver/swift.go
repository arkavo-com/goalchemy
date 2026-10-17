package driver

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	swiftemit "github.com/eugenioenko/goalchemy/internal/emit/swift"
	"github.com/eugenioenko/goalchemy/internal/link"
)

func init() { Register("swift", emitSwift) }

const swiftRun = `#!/bin/sh
set -eu
cd "$(dirname "$0")"
mkdir -p .build/native
${CC:-cc} -O2 -Wno-deprecated-declarations -c rt/native/GoalchemyNative.c -o .build/native/native.o $(pkg-config --cflags openssl zlib libcurl)
build_swift() {
    @GOALCHEMY_SWIFT_SOURCES@
    ${SWIFTC:-swiftc} -swift-version 5 -suppress-warnings -I rt/native "$@" .build/native/native.o $(pkg-config --libs openssl zlib libcurl) -o .build/program
}
build_swift
exec .build/program "$@"
`

const swiftLibraryBuild = `#!/bin/sh
set -eu
cd "$(dirname "$0")"
exec swift build -c release "$@"
`

const swiftPackage = `// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "GoalchemyGenerated",
@GOALCHEMY_SWIFT_PLATFORMS@    products: [.library(name: "GoalchemyGenerated", targets: ["GoalchemyGenerated"])],
    targets: [
        .systemLibrary(name: "COpenSSL", path: "system/openssl", pkgConfig: "openssl",
                       providers: [.apt(["libssl-dev"]), .brew(["openssl@3"])]),
        .systemLibrary(name: "CZlib", path: "system/zlib", pkgConfig: "zlib",
                       providers: [.apt(["zlib1g-dev"]), .brew(["zlib"])]),
        .systemLibrary(name: "CCurl", path: "system/curl", pkgConfig: "libcurl",
                       providers: [.apt(["libcurl4-openssl-dev"]), .brew(["curl"])]),
        .target(name: "GoalchemyNative", dependencies: ["COpenSSL", "CZlib", "CCurl"],
                path: "rt/native", sources: ["GoalchemyNative.c"], publicHeadersPath: ".",
                linkerSettings: [.linkedLibrary("crypto"), .linkedLibrary("z"), .linkedLibrary("curl")]),
        .target(name: "GoalchemyGenerated", dependencies: ["GoalchemyNative"], path: ".",
                exclude: ["rt/native", "system", "README.md", "LICENSE", "build.sh", @GOALCHEMY_SWIFT_MAPS@"goalchemy.manifest.json"],
                sources: [@GOALCHEMY_SWIFT_SOURCES@])
    ],
    swiftLanguageModes: [.v5]
)
`

func emitSwift(res *Result, out string) []diagnostics.Diagnostic {
	o, err := swiftemit.Emit(res.IR, symbols(res, "swift"))
	if err != nil {
		var boundary *swiftemit.LibraryBoundaryError
		if errors.As(err, &boundary) {
			return emitErr("GCE007", err.Error())
		}
		return emitErr("GCE004", err.Error())
	}
	if res.ObserveAPI != nil {
		res.ObserveAPI(o.API)
	}
	refs, files, ds := link.Plan(res.Catalog, "swift", o.Contracts)
	if len(ds) > 0 {
		return ds
	}
	runtimeFiles, err := link.CopyRuntime(res.Catalog, "swift", files, out, "rt", false)
	if err != nil {
		return emitErr("GCE005", err.Error())
	}
	names, err := writeSourceArtifacts(out, o.Files)
	if err != nil {
		return emitErr("GCE005", err.Error())
	}
	var sources []string
	var sourceArgs strings.Builder
	sourceArgs.WriteString("set --")
	var maps []string
	for _, file := range append(append([]string(nil), names...), runtimeFiles...) {
		if strings.HasSuffix(file, ".swift") {
			sources = append(sources, fmt.Sprintf("%q", file))
			sourceArgs.WriteString(" \\\n        '" + strings.ReplaceAll(file, "'", "'\\''") + "'")
		}
		if strings.HasSuffix(file, ".lines") {
			maps = append(maps, fmt.Sprintf("%q, ", file))
		}
	}
	native := ""
	if res.IR.PauseModel == "native" {
		native = " The native pause model uses Swift task executors: on Apple platforms it requires macOS 15, iOS 18, tvOS 18, watchOS 11 or visionOS 2."
	}
	generated := map[string][]byte{
		"run.sh":    []byte(strings.Replace(swiftRun, "@GOALCHEMY_SWIFT_SOURCES@", sourceArgs.String(), 1)),
		"README.md": []byte(readme("swift", "sh run.sh", "Requires Swift 6.4 or later, a C compiler, pkg-config, OpenSSL 3, zlib, and libcurl development headers/libraries. Native dependencies are currently included in every Swift output. The verified baseline is Linux x86_64; Apple SDK integration is not yet verified."+native)),
	}
	if res.IR.Library {
		delete(generated, "run.sh")
		generated["build.sh"] = []byte(swiftLibraryBuild)
		packageSource := strings.Replace(swiftPackage, "@GOALCHEMY_SWIFT_SOURCES@", strings.Join(sources, ", "), 1)
		packageSource = strings.Replace(packageSource, "@GOALCHEMY_SWIFT_MAPS@", strings.Join(maps, ""), 1)
		platforms := ""
		if res.IR.PauseModel == "native" {
			platforms = "    platforms: [.macOS(.v15), .iOS(.v18), .tvOS(.v18), .watchOS(.v11), .visionOS(.v2)],\n"
		}
		packageSource = strings.Replace(packageSource, "@GOALCHEMY_SWIFT_PLATFORMS@", platforms, 1)
		generated["Package.swift"] = []byte(packageSource)
		generated["system/openssl/module.modulemap"] = []byte("module COpenSSL [system] {\n    header \"shim.h\"\n    link \"crypto\"\n    export *\n}\n")
		generated["system/openssl/shim.h"] = []byte("#include <openssl/evp.h>\n")
		generated["system/zlib/module.modulemap"] = []byte("module CZlib [system] {\n    header \"shim.h\"\n    link \"z\"\n    export *\n}\n")
		generated["system/zlib/shim.h"] = []byte("#include <zlib.h>\n")
		generated["system/curl/module.modulemap"] = []byte("module CCurl [system] {\n    header \"shim.h\"\n    link \"curl\"\n    export *\n}\n")
		generated["system/curl/shim.h"] = []byte("#include <curl/curl.h>\n")
		generated["README.md"] = []byte(readme("swift", "sh build.sh", "SwiftPM library product/module GoalchemyGenerated. Add this directory as a local package dependency or publish it under your SDK package name. Requires Swift 6.4 or later, OpenSSL 3, zlib, and libcurl development files. Native dependency discovery uses pkg-config. Verified on Linux x86_64; Apple SDK integration is not yet verified."+native))
	}
	license, err := fs.ReadFile(res.Catalog.FS, "LICENSE")
	if err != nil {
		return emitErr("GCE005", err.Error())
	}
	generated["LICENSE"] = license
	var metadata []string
	for name := range generated {
		metadata = append(metadata, name)
	}
	sort.Strings(metadata)
	for _, name := range metadata {
		if err := link.WriteFile(out, name, generated[name]); err != nil {
			return emitErr("GCE005", err.Error())
		}
	}
	names = append(names, metadata...)
	sort.Strings(names)
	if err := link.WriteManifest(out, res.Catalog, "swift", refs, runtimeFiles, names, res.Program, o.Packages...); err != nil {
		return emitErr("GCE005", err.Error())
	}
	return nil
}
