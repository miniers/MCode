// Package procattr 统一调整子进程的平台相关属性。
//
// 目前只做一件事：Windows 上让控制台子进程不弹出控制台窗口。
// 本程序以 -H windowsgui 构建，是纯 GUI 进程；它启动 git、gh、node、claude 等
// 控制台程序时，系统默认会为每个子进程新开一个控制台窗口，导致频繁闪窗。
// 调用方在 Start/Run/Output/CombinedOutput 之前套一层 HideWindow 即可。
package procattr
