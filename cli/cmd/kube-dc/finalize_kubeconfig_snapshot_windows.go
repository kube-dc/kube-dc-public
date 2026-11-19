//go:build windows

package main

// privateDirectory: file modes say nothing about access on Windows; the
// directory is under the user's profile, which is private by default.
func privateDirectory(string) error { return nil }

// processGone: not determined on Windows, so nothing is swept there.
func processGone(int) bool { return false }
