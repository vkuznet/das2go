// das2go - Go implementation of Data Aggregation System (DAS) for CMS
//
// Copyright (c) 2015-2016 - Valentin Kuznetsov <vkuznet@gmail.com>
//
package main

import (
	"flag"
	"github.com/vkuznet/das2go/utils"
	"github.com/vkuznet/das2go/web"
)

func main() {
	var afile string
	flag.StringVar(&afile, "afile", "", "DAS authentication key file")
	var port string
	flag.StringVar(&port, "port", "8212", "DAS server port number")
	var verbose int
	flag.IntVar(&verbose, "verbose", 0, "Verbose level, support 0,1,2")
	var urlQueueLimit int
	flag.IntVar(&urlQueueLimit, "urlQueueLimit", 1000, "urlQueueLimit controls number of concurrent URL calls to remote data-services, default 1000")
	flag.Parse()
	utils.VERBOSE = verbose
	utils.UrlQueueLimit = int32(urlQueueLimit)
	web.Server(port, afile)
}
