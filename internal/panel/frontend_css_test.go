package panel

import (
	"strings"
	"testing"
)

// TestPanelCSSContracts 钉住几条「只在 CSS 里成立、行为测试覆盖不到」的契约。
//
// 为什么单独测样式：app_harness.js 只加载 app.js（行为），index.html 的样式它看不见。
// 而下面几件事都属于「写错了页面看着也正常，只是手感不对」——正是最容易悄悄回归的一类。
func TestPanelCSSContracts(t *testing.T) {
	css := string(indexHTML)

	// 1) 队列「运行中」小圆点的脉动。`.qdot.run` 引用了 `@keyframes qpulse`，而这个 keyframes
	//    曾经**根本不存在**——浏览器对未知动画名静默忽略，于是那个点永远不闪。引用与定义必须成对。
	if strings.Contains(css, "animation: qpulse") && !strings.Contains(css, "@keyframes qpulse") {
		t.Error("`.qdot.run` 引用了 qpulse，但 index.html 里没有 @keyframes qpulse 定义——那个小圆点不会闪")
	}

	// 2) 域开关的滑动胶囊：需要一个绝对定位的指示器 + transform 过渡，
	//    否则「点了没动画」会悄悄回来（app.js 侧已保证不重建 DOM，样式是另一半）。
	if !strings.Contains(css, "#realmSwitch .chip-ind") {
		t.Error("缺少域开关滑动胶囊的样式（#realmSwitch .chip-ind）")
	}
	if !strings.Contains(css, "#realmSwitch { position: relative; }") {
		t.Error("#realmSwitch 必须是 position: relative——胶囊按 offsetLeft 定位，少了它胶囊会错位")
	}
	if !strings.Contains(css, "transition: transform .18s") {
		t.Error("胶囊缺少 transform 过渡（切域时不会滑动）")
	}

	// 3) 左侧导航项的底色过渡（切页那一下不再硬切）。
	navIdx := strings.Index(css, ".nav a {")
	if navIdx < 0 {
		t.Fatal("找不到 .nav a 规则")
	}
	block := css[navIdx:min(navIdx+400, len(css))]
	if !strings.Contains(block, "transition: background") {
		t.Error("左侧导航项缺少底色过渡（.nav a 的 transition）")
	}
}
