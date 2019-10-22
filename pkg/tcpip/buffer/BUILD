load("@io_bazel_rules_go//go:def.bzl", "go_test")
load("//tools/go_stateify:defs.bzl", "go_library")

package(licenses = ["notice"])

go_library(
    name = "buffer",
    srcs = [
        "packet_buffer.go",
        "packet_buffer_state.go",
        "prependable.go",
        "view.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/tcpip/buffer",
    visibility = ["//visibility:public"],
)

go_test(
    name = "buffer_test",
    size = "small",
    srcs = ["view_test.go"],
    embed = [":buffer"],
)
