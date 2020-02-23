package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	mp "github.com/mackerelio/go-mackerel-plugin-helper"
	"golang.org/x/time/rate"
)

type NginxCachePlugin struct {
	ProxyCachePath         string
	ProxyCacheSize         uint64
	ProxyCacheKeysZoneName string
	Tempfile               string
}

var (
	usageUnitPat *regexp.Regexp
)

const (
	statPerSec = 100000 // 1秒あたりのstat回数
)

func init() {
	usageUnitPat = regexp.MustCompile("m$")
}

func buildTempfilePath(path string) string {
	return fmt.Sprintf("/tmp/mackerel-plugin-nginx-cache-%s", strings.Replace(path, "/", "-", -1))
}

func diskUsage(ctx context.Context, limiter *rate.Limiter, dir string, maxDepth, depth int) (int64, error) {
	usage := int64(0)
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return usage, err
	}

	for _, file := range files {
		if err := limiter.Wait(ctx); err != nil {
			return usage, err
		}
		if file.IsDir() {
			dirUsage, err := diskUsage(ctx, limiter, filepath.Join(dir, file.Name()), maxDepth, depth+1)
			if err != nil {
				return usage, err
			}
			usage += dirUsage
		} else {
			if statt, ok := file.Sys().(*syscall.Stat_t); ok {
				usage += int64(statt.Blksize)
			}
		}
	}
	return usage, nil
}

func (n NginxCachePlugin) FetchMetrics() (map[string]interface{}, error) {
	ctx := context.Background()
	r := rate.Every(time.Second / statPerSec)
	limiter := rate.NewLimiter(r, statPerSec)
	usageByte, err := diskUsage(ctx, limiter, n.ProxyCachePath, 10, 0)
	if err != nil {
		return nil, err
	}
	usage := uint64(float64(usageByte) / (1024 * 1024))

	stat := make(map[string]interface{})
	stat["size"] = n.ProxyCacheSize
	stat["usage"] = usage

	return stat, nil
}

func (n NginxCachePlugin) GraphDefinition() map[string](mp.Graphs) {
	dk := fmt.Sprintf("nginx-cache.disk-%s", n.ProxyCacheKeysZoneName)

	var graphdef map[string](mp.Graphs) = map[string](mp.Graphs){
		dk: mp.Graphs{
			Label: fmt.Sprintf("nginx cache usage megabyte: %s", n.ProxyCachePath),
			Unit:  "integer",
			Metrics: [](mp.Metrics){
				mp.Metrics{Name: "usage", Label: "Usage", Diff: false, Type: "uint64"},
				mp.Metrics{Name: "size", Label: "Size", Diff: false, Type: "uint64"},
			},
		},
	}

	return graphdef
}

func main() {
	proxyCachePath := flag.String("path", "", "proxy_cache_path $path")
	proxyCacheSize := flag.String("size", "", "proxy_cache_path $max_size")
	proxyCacheKeysZoneName := flag.String("kname", "", "proxy_cache_path $keys_zone_name")
	tempfile := flag.String("tempfile", "", "temporary file path")
	flag.Parse()

	var (
		nginx NginxCachePlugin
		err   error
	)

	nginx.ProxyCachePath = *proxyCachePath
	nginx.ProxyCacheKeysZoneName = *proxyCacheKeysZoneName

	if usageUnitPat.MatchString(*proxyCacheSize) {
		proxyCacheSizeStr := *proxyCacheSize
		*proxyCacheSize = proxyCacheSizeStr[:len(proxyCacheSizeStr)-1]
	}
	nginx.ProxyCacheSize, err = strconv.ParseUint(*proxyCacheSize, 0, 64)
	if err != nil {
		os.Exit(1)
	}

	helper := mp.NewMackerelPlugin(nginx)

	if *tempfile != "" {
		helper.Tempfile = *tempfile
	} else {
		helper.Tempfile = buildTempfilePath(*proxyCachePath)
	}

	if os.Getenv("MACKEREL_AGENT_PLUGIN_META") != "" {
		helper.OutputDefinitions()
	} else {
		helper.OutputValues()
	}
}
