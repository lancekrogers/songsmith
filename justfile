#!/usr/bin/env just --justfile
# Songsmith: write and render songs with local models

binary_name := "songsmith"
bin_dir := "bin"

[doc('Testing and lint')]
mod test '.justfiles/test.just'

import '.justfiles/dev.just'

default:
    @just --list --unsorted
