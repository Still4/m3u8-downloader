//@author:llychao<lychao_vip@163.com>
//@date:2020-02-18
//@功能:golang m3u8 video Downloader

//@update author: still4
//@update date: 2021-03-16
//@update info: 增加数据校验，连续2次读取数据长度一致才会写入
//@exist prob: 并发导数据会导致数据缺失，暂时已关停
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"flag"
	"fmt"
	"github.com/levigross/grequests"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	//HeadTimeout 请求头超时时间
	HEAD_TIMEOUT = 10 * time.Second
	//进度条长度
	progressWidth = 40
)

var (
	//命令行参数
	urlFlag = flag.String("u", "", "m3u8下载地址(http(s)://url/xx/xx/index.m3u8)")
	nFlag   = flag.Int("n", 16, "下载线程数(max goroutines num)")
	htFlag  = flag.String("ht", "apiv1", "设置getHost的方式(apiv1: `http(s):// + url.Host + path.Dir(url.Path)`; apiv2: `http(s)://+ u.Host`")
	oFlag   = flag.String("o", "temp", "自定义文件名(默认为temp)")
	cFlag   = flag.String("c", "", "自定义请求cookie")

	logger *log.Logger
)

//TsInfo 用于保存ts文件的下载地址和文件名
type TsInfo struct {
	Name string
	Url  string
}

func init() {
	logger = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)
}

func main() {
	Run()
}

func Run() {
	msgTpl := "[功能]:多线程下载直播流m3u8的视屏（ts+合并）\n[提醒]:如果下载失败，请使用-ht=apiv2\n[提醒]:如果下载失败，m3u8地址可能存在嵌套\n[提醒]:如果进度条中途下载失败，可重复执行"
	fmt.Println(msgTpl)
	runtime.GOMAXPROCS(runtime.NumCPU())
	now := time.Now()

	//解析命令行参数
	flag.Parse()
	m3u8Url := *urlFlag
	maxGoroutines := *nFlag
	hostType := *htFlag
	targetDir := *oFlag

	if !strings.HasPrefix(m3u8Url, "http") || !strings.Contains(m3u8Url, "m3u8") || m3u8Url == "" {
		flag.Usage()
		return
	}

	pwd, _ := os.Getwd()
	//pwd = "/Users/chao/Desktop" //自定义地址
	dir := pwd + "/download/" + targetDir
	isExist, _ := PathExists(dir)
	if !isExist {
		os.MkdirAll(dir, os.ModePerm)
	}
	//删除临时目录
	//_ = RemoveContents(dir)

	m3u8Host := getHost(m3u8Url, hostType)
	m3u8Body := getM3u8Body(m3u8Url)

	tsKey := getM3u8Key(m3u8Host, m3u8Body)
	if tsKey != "" {
		fmt.Printf("文件已加密，解密密钥: %s \n", tsKey)
	} else {
		fmt.Printf("文件未加密 \n")
	}

	tsList := getTsList(m3u8Host, m3u8Body)
	fmt.Println("待下载ts文件数量:", len(tsList))

	//下载ts
	DownloadTsFile(tsList, maxGoroutines, dir, tsKey)

	MergeFile(dir)

	os.Rename(dir+"/merge.mp4", dir+".mp4")
	//os.RemoveAll(dir)

	DrawProgressBar("Merging", float32(1), progressWidth, "merge.ts")
	fmt.Printf("\nDone! 耗时:%6.2fs\n", time.Now().Sub(now).Seconds())
}

func RemoveContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}

func getRequestOptions() *grequests.RequestOptions {
	ro := &grequests.RequestOptions{
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Safari/537.36",
		RequestTimeout: HEAD_TIMEOUT,
		Headers: map[string]string{
			"Connection":      "keep-alive",
			"Accept":          "*/*",
			"Accept-Encoding": "*",
			"Accept-Language": "zh-Hans;q=1",
		},
	}
	cookie := *cFlag

	//http自定义cookie
	if cookie != "" {
		ro.Headers["Cookie"] = cookie
	}
	return ro
}

//获取m3u8地址的host
func getHost(Url, ht string) (host string) {
	u, err := url.Parse(Url)
	checkErr(err)
	switch ht {
	case "apiv1":
		host = u.Scheme + "://" + u.Host + path.Dir(u.Path)
	case "apiv2":
		host = u.Scheme + "://" + u.Host
	}
	return
}

//获取m3u8地址的内容体
func getM3u8Body(Url string) string {
	r, err := grequests.Get(Url, getRequestOptions())
	checkErr(err)
	return r.String()
}

//获取m3u8加密的密钥
func getM3u8Key(host, html string) (key string) {
	lines := strings.Split(html, "\n")
	key = ""
	for _, line := range lines {
		if strings.Contains(line, "#EXT-X-KEY") {
			uri_pos := strings.Index(line, "URI")
			quotation_mark_pos := strings.LastIndex(line, "\"")
			key_url := strings.Split(line[uri_pos:quotation_mark_pos], "\"")[1]
			if !strings.Contains(line, "http") {
				key_url = fmt.Sprintf("%s/%s", host, key_url)
			}
			res, err := grequests.Get(key_url, getRequestOptions())
			checkErr(err)
			if res.StatusCode == 200 {
				key = res.String()
			}
		}
	}
	return
}

func getTsList(host, body string) (tsList []TsInfo) {
	lines := strings.Split(body, "\n")
	index := 0
	var ts TsInfo

	for _, line := range lines {
		if !strings.HasPrefix(line, "#") && line != "" {
			//有可能出现的二级嵌套格式的m3u8,请自行转换！
			index++
			if strings.HasPrefix(line, "http") {
				ts = TsInfo{
					Name: fmt.Sprintf("%05d.ts", index),
					Url:  line,
				}
				tsList = append(tsList, ts)
			} else {
				ts = TsInfo{
					Name: fmt.Sprintf("%05d.ts", index),
					Url:  fmt.Sprintf("%s/%s", host, line),
				}
				tsList = append(tsList, ts)
			}
		}
	}
	return
}

//判断文件是否存在
func PathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

//下载ts文件
//modify: 2020-08-13 修复ts格式SyncByte合并不能播放问题
func GetContent(info TsInfo, retries int) []byte {
	//defer func() {
	//	if r := recover(); r != nil {
	//		//fmt.Println("网络不稳定，正在进行断点持续下载")
	//		GetContent(ts, retries-1)
	//	}
	//}()
	logger.Printf("[info] Download File %s: %s", info.Name, info.Url)

	//直到两次下载数据完全一致
	var content []byte
	var checkContent []byte
	for i := 0; i < retries; i++ {
		res, err := grequests.Get(info.Url, getRequestOptions())
		if err == nil && res.Ok {
			content = res.Bytes()
			logger.Printf("[info] Get File %s(%d/%d): Size %d", info.Name, i, retries, len(content))
		} else {
			logger.Printf("[info] Get File %s(%d/%d): Fail", info.Name, i, retries)
		}
		if len(content) == len(checkContent) {
			return content
		} else {
			checkContent = content
		}
	}
	return nil
}

func WriteFile(content []byte, priKey string, dir string, info TsInfo) {
	var err error
	filePath := filepath.Join(dir, info.Name)
	existContent, err := ioutil.ReadFile(filePath)
	if err == nil && len(existContent) == len(content) {
		logger.Printf("[info] Write File :%s Exist", info.Name)
		return
	}
	//删除已有文件
	err = os.RemoveAll(filePath)
	if err != nil {
		logger.Printf("[error] Write File :%s Cannot Delete Exist File", info.Name)
	}
	var writeContent []byte
	if priKey == "" {
		writeContent = content
	} else {
		//若加密，解密ts文件 aes 128 cbc pack5
		writeContent, err = AesDecrypt(content, []byte(priKey))
		if err != nil {
			logger.Printf("[error] Decrypt File :%s Fail", info.Name)
			return
		}
	}
	if (len(writeContent) == 0) {
		logger.Printf("[error] Empty File :%s", info.Name)
		return
	}

	// https://en.wikipedia.org/wiki/MPEG_transport_stream
	// Some TS files do not start with SyncByte 0x47, they can not be played after merging,
	// Need to remove the bytes before the SyncByte 0x47(71).
	syncByte := uint8(71) //0x47
	bLen := len(writeContent)
	for j := 0; j < bLen; j++ {
		if writeContent[j] == syncByte {
			writeContent = writeContent[j:]
			break
		}
	}
	err = ioutil.WriteFile(filePath, writeContent, 0666)
	if err != nil {
		logger.Printf("[error] Write File :%s Fail", info.Name)
	}
}

//DownloadTsFile m3u8下载器
func DownloadTsFile(tsList []TsInfo, maxGoroutines int, dir string, priKey string) {
	retry := 20 //单个ts下载重试次数
	//并行有bug，暂时改为单线程
	maxGoroutines = 1
	var wg sync.WaitGroup
	limiter := make(chan struct{}, maxGoroutines) //chan struct 内存占用0 bool占用1

	totalCount := len(tsList)
	handleCount := 0

	for _, ts := range tsList {
		//info := fmt.Sprintf("%s: %s", ts.Name, ts.Url)
		//fmt.Print("\n" + info)
		wg.Add(1)
		limiter <- struct{}{}
		go func(ts TsInfo, dir, priKey string, retries int) {
			defer func() {
				wg.Done()
				<-limiter
			}()
			content := GetContent(ts, retries)
			WriteFile(content, priKey, dir, ts)
			handleCount++
			DrawProgressBar("Downloading", float32(handleCount)/float32(totalCount), progressWidth, ts.Name)
			return
		}(ts, dir, priKey, retry)
		//break
	}
	wg.Wait()
}

//进度条
func DrawProgressBar(prefix string, proportion float32, width int, suffix ...string) {
	//pos := int(proportion * float32(width))
	//s := fmt.Sprintf("[%s] %s%*s %6.2f%% \t%s",
	//	prefix, strings.Repeat("■", pos), width-pos, "", proportion*100, strings.Join(suffix, ""))
	//fmt.Print("\r" + s)
}

// ============================== shell相关 ==============================

//执行shell
func ExecUnixShell(s string) {
	cmd := exec.Command("/bin/bash", "-c", s)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s", out.String())
}

func ExecWinShell(s string) error {
	cmd := exec.Command("cmd", "/C", s)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return err
	}
	fmt.Printf("%s", out.String())
	return nil
}

//windows合并文件
func MergeFile(path string) {
	if runtime.GOOS == "windows" {
		os.Chdir(path)
		ExecWinShell("copy /b *.ts merge.tmp")
		ExecWinShell("del /Q *.ts")
		os.Rename("merge.tmp", "merge.mp4")
	} else {
		os.Chdir(path)
		//cmd := `ls  *.ts |sort -t "\." -k 1 -n |awk '{print $0}' |xargs -n 1 -I {} bash -c "cat {} >> new.tmp"`
		cmd := `cat *.ts >> merge.tmp`
		ExecUnixShell(cmd)
		//ExecUnixShell("rm -rf *.ts")
		os.Rename("merge.tmp", "merge.mp4")
	}
}

// ============================== 加解密相关 ==============================

func PKCS7Padding(ciphertext []byte, blockSize int) []byte {
	padding := blockSize - len(ciphertext)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(ciphertext, padtext...)
}

func PKCS7UnPadding(origData []byte) []byte {
	length := len(origData)
	unpadding := int(origData[length-1])
	return origData[:(length - unpadding)]
}

func AesEncrypt(origData, key []byte, ivs ...[]byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	var iv []byte
	if len(ivs) == 0 {
		iv = key
	} else {
		iv = ivs[0]
	}
	origData = PKCS7Padding(origData, blockSize)
	blockMode := cipher.NewCBCEncrypter(block, iv[:blockSize])
	crypted := make([]byte, len(origData))
	blockMode.CryptBlocks(crypted, origData)
	return crypted, nil
}

func AesDecrypt(crypted, key []byte, ivs ...[]byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	var iv []byte
	if len(ivs) == 0 {
		iv = key
	} else {
		iv = ivs[0]
	}
	blockMode := cipher.NewCBCDecrypter(block, iv[:blockSize])
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	origData = PKCS7UnPadding(origData)
	return origData, nil
}

func checkErr(e error) {
	if e != nil {
		logger.Panic(e)
	}
}
