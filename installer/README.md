# Windows 安装包

使用 Inno Setup 6 构建，安装包按当前用户安装，不需要管理员权限。

## 行为

- 默认安装到 `%LOCALAPPDATA%\Programs\PicDup`
- 创建开始菜单快捷方式；桌面快捷方式可选
- 安装包会检测 WebView2 Runtime，缺失时打开微软下载入口
- 升级使用同一个 `AppId`，会保留已有设置
- 卸载时保留 `PicDupData`，不会删除设置、缓存或隔离区
- 简体中文界面；也保留英文语言选项

## 构建

先构建 `bin\PicDup.exe`，再运行：

```powershell
ISCC.exe installer\PicDup.iss
```

输出：

```text
dist\PicDup-v1.0.0-win64-setup.exe
```

## 中文语言文件

`ChineseSimplified.isl` 来自 Inno Setup 的简体中文翻译：

- Maintainer: Zhenghan Yang (Kira)
- https://github.com/kira-96/Inno-Setup-Chinese-Simplified-Translation

该文件仅用于构建安装包界面。
