package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TZ 北京时间(与旧版 Python timezone(timedelta(hours=8)) 一致)
var TZ = time.FixedZone("CST", 8*3600)

const (
	portalURL  = "https://aita.yanhekt.cn/education/?tenant_code=21#/home"
	courseAPI  = "https://aita.yanhekt.cn/personal/courseapi/vlabpassportapi/v1/account-profile/course?nowpage=1&per-page=100"
	eclassBase = "https://zy-eclass.bit.edu.cn"
	eclassHome = eclassBase + "/user/index#/"

	loginWait = 10 * time.Minute // 等待人工登录的最长时间
	portFirst = 8765             // 本地服务端口范围
	portLast  = 8769
	taskName  = "YanhektDDLMonitor"
	exeName   = "yanhektddl.exe"
)

// 运行期路径(基于 exe 所在目录;go run 时回退到工作目录)
var (
	base         string
	profileDir   string
	dataJSON     string
	settingsJSON string
	reportMD     string
	reportCSV    string

	logger = log.New(os.Stdout, "", 0) // 桌面模式下会切换到 server.log
)

func init() {
	base = computeBaseDir()
	profileDir = filepath.Join(base, "browser_profile")
	dataJSON = filepath.Join(base, "ddl_data.json")
	settingsJSON = filepath.Join(base, "settings.json")
	reportMD = filepath.Join(base, "ddl_report.md")
	reportCSV = filepath.Join(base, "ddl_report.csv")
}

func computeBaseDir() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Clean(filepath.Dir(exe))
		tmp := filepath.Clean(os.TempDir())
		if !strings.HasPrefix(dir, tmp) { // go run 的 exe 在临时目录 -> 用工作目录
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// initFileLog 桌面模式:日志写入 server.log,避免无控制台时丢失排错信息。
//
// 文件本身是 UTF-8(Go 字符串即 UTF-8 字节),但 Windows 记事本等编辑器
// 对无 BOM 的文本默认按 ANSI(GBK)解码,打开就是乱码。新建文件时写入
// UTF-8 BOM,编码识别即成定局;已有内容的旧日志不改动(中插 BOM 反而
// 制造乱码),退出程序后删掉旧 server.log 即可获得带 BOM 的新文件。
func initFileLog() {
	f, err := os.OpenFile(filepath.Join(base, "server.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	// 文件为空(新建或刚被清空)时写入 BOM;已有内容则不动
	if st, serr := f.Stat(); serr == nil && st.Size() == 0 {
		f.Write([]byte{0xEF, 0xBB, 0xBF})
	}
	logger = log.New(f, "", log.LstdFlags)
}
