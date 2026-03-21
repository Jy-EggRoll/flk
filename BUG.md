# 已知问题记录

## 2026-03-21

使用 fix 模式时，如果需要恢复的位置已经是一个符号链接，且指向已失效的位置，会导致恢复失败。

btop -> /home/abc/GitRepo/my-config/btop-配置-class-linux

例如 fix 想要覆写 btop 下的文件 btop/config，但是失败了。

这是由于，flx 本身恢复时的底层能力是尊重符号链接的，btop 指向了一个已经失效的位置，所以 flx 无法正确地恢复文件。
