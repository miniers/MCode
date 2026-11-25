package designpick

import _ "embed"

// Script 是注入到设计稿预览页中的方案点选脚本。
//
//go:embed picker.js
var Script string
