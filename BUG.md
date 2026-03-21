# 已知问题记录

## 26=03=21

使用 fix 模式时，如果需要恢复的位置已经是一个符号链接，且指向错误（已失效）的位置，会导致恢复失败。

btop -> /home/abc/GitRepo/my-config/btop-配置-class-linux

例如 fx 想要覆写 btop，但是失败了。
