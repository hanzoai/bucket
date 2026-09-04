// github.com/hanzoai/bucket is a standalone, stdlib-only module so any
// product can import the client without pulling cloud's server dependency
// graph into its own binary. go 1.24 (not a newer toolchain) keeps the
// import compatible with every Hanzo service regardless of its own Go
// version.
module github.com/hanzoai/bucket

go 1.24
