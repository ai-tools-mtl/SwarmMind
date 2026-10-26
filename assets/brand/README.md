# SwarmMind 品牌资源

本组资源根据已确认的「协作之核」概念整理为平面矢量。保留四个咬合模块、S 形轮廓、中央负形与靛蓝/青绿配色；去除概念图中的光影和演示排版。

## 文件

| 文件 | 用途 |
|---|---|
| `logo-mark.svg` / `.png` | 主图标；PNG 为 512×512 透明底，可用于仓库头像等独立图标位置 |
| `logo-lockup.svg` / `.png` | 图标与字标横排，1200×300 透明底 |
| `logo-github.svg` / `.png` | 仓库 README 专用浅色展示横幅，1400×400 |
| `logo-github-dark.svg` / `.png` | 仓库 README 专用深色横幅，1400×400 |
| `logo-mono.svg` / `.png` | 深墨单色图标，透明底 |
| `logo-white.svg` / `.png` | 白色反白图标，透明底，供深色底使用 |
| `wordmark-outline.svg` | 已转路径的字标源，不需要安装字体 |

配色：靛蓝 `#4F46E5`、青绿 `#22D3C5`、深墨 `#172033`、白色 `#FFFFFF`。最小留白建议为图标宽度的 1/8；避免拉伸、旋转或改变各模块相对位置。

## 系统与仓库接入

- `assets/brand.go` 编译嵌入主图标，hub/node 通过 `/logo.svg` 与 `/favicon.svg` 提供同一 SVG。Logo 为公开静态资源，不包含运行数据。
- 两端顶部使用 30px、侧栏 24px、登录页 64px；浏览器按 favicon 声明使用 SVG。
- 根 README 的 picture 元素分别引用浅色和深色 GitHub 横幅，显示宽度 700px。
- PNG 位于本目录，未落入 `docs/**/*.png` 的截图忽略规则；无需强制 git add 或修改截图规则。
- 本轮仅将资源写入工作区和接入代码，不自动执行 Git 提交/推送，也不会替换远端仓库头像。

## 维护

执行 `python3 scripts/build-brand.py` 可从脚本中的统一几何与路径字标重新导出六个 SVG，使用 Python 标准库即可，无需字体或网络。主标志四个模块的顶点是可编辑源，字标源保存在 wordmark-outline.svg。

PNG 必须从对应 SVG 渲染，不能从概念提案板截图裁剪。修改 SVG 后同步更新 PNG 并视觉核对透明背景、小尺寸和明暗版；运行资源改动须重建 hub/node，再进行真实页面走查。

本轮验证见 [Logo 接入报告](../../docs/TESTREPORT-logo.md)。
