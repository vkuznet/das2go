/*
 *
 * Author     : Valentin Kuznetsov <vkuznet AT gmail dot com>
 * Description: DAS core module
 * Created    : Fri Jun 26 14:25:01 EDT 2015
 */
package das

import (
	"dasmaps"
	"dasql"
	"fmt"
	"gopkg.in/mgo.v2/bson"
	"log"
	"mongo"
	"net/url"
	"reflect"
	"regexp"
	"services"
	"strings"
	"time"
	"utils"
)

type Record map[string]interface{}
type DASRecord struct {
	query  dasql.DASQuery
	record Record
	das    Record
}

func (r *DASRecord) Qhash() string {
	return string(r.query.Qhash)
}

func (r *DASRecord) Services() []string {
	return []string{}
}

// Extract API call parameters from das map entry
func getApiParams(dasmap mongo.DASRecord) (string, string, string, string) {
	das_key, ok := dasmap["das_key"].(string)
	if !ok {
		das_key = ""
	}
	rec_key, ok := dasmap["rec_key"].(string)
	if !ok {
		rec_key = ""
	}
	api_arg, ok := dasmap["api_arg"].(string)
	if !ok {
		api_arg = ""
	}
	pattern, ok := dasmap["pattern"].(string)
	if !ok {
		pattern = ""
	}
	return das_key, rec_key, api_arg, pattern
}

// Form appropriate URL from given dasquery and dasmap, the final URL
// contains all parameters
func formUrlCall(dasquery dasql.DASQuery, dasmap mongo.DASRecord) string {
	spec := dasquery.Spec
	skeys := utils.MapKeys(spec)
	base, ok := dasmap["url"].(string)
	if !strings.HasPrefix(base, "http") {
		return "local_api"
	}
	// Exception block, current DAS maps contains APIs which should be treated
	// as local apis, e.g. file_run_lumi4dataset in DBS3 maps. In a future
	// I'll need to fix DBS3 maps to make it local_api
	// For time being I'll list those exceptional APIs in DASLocalAPIs list
	urn, _ := dasmap["urn"].(string)
	if utils.InList(urn, services.DASLocalAPIs()) {
		return "local_api"
	}
	// TMP, until we change phedex maps to use JSON
	if strings.Contains(base, "phedex") {
		base = strings.Replace(base, "xml", "json", -1)
	}
	if !ok {
		log.Fatal("Unable to extract url from DAS map", dasmap)
	}
	dasmaps := dasmaps.GetDASMaps(dasmap["das_map"])
	vals := url.Values{}
	var use_args []string
	system, _ := dasmap["system"].(string)
	for _, dmap := range dasmaps {
		dkey, rkey, arg, pat := getApiParams(dmap)
		if utils.InList(dkey, skeys) {
			val, ok := spec[dkey].(string)
			if ok {
				matched, _ := regexp.MatchString(pat, val)
				if matched || pat == "" {
					// exception for lumi_list input parameter, files DBS3 API accept only lists of lumis
					if system == "dbs3" && arg == "lumi_list" {
						vals.Add(arg, fmt.Sprintf("[%s]", val))
					} else {
						vals.Add(arg, val)
					}
					use_args = append(use_args, arg)
				}
			} else { // let's try array of strings
				arr, ok := spec[dkey].([]string)
				if !ok {
					log.Println("WARNING, unable to get value(s) for daskey=", dkey,
						", reckey=", rkey, " from spec=", spec, " das map=", dmap)
				}
				for _, val := range arr {
					matched, _ := regexp.MatchString(pat, val)
					if matched || pat == "" {
						vals.Add(arg, val)
						use_args = append(use_args, arg)
					}
				}
			}
		}
	}
	// loop over params in DAS maps and add additional arguments which have
	// non empty, non optional and non required values
	skipList := []string{"optional", "required"}
	params := dasmap["params"].(mongo.DASRecord)
	for key, val := range params {
		vvv := val.(string)
		if !utils.InList(key, use_args) && !utils.InList(vvv, skipList) {
			vals.Add(key, vvv)
		}
	}

	// Encode all arguments for url
	args := vals.Encode()
	if len(vals) < len(skeys) {
		return "" // number of arguments should be equal or more number of spec key values
	}
	if len(args) > 0 {
		return base + "?" + args
	}
	return base
}

type DASRecords []mongo.DASRecord

// helper function to process given set of URLs associted with dasquery
func processLocalApis(dasquery dasql.DASQuery, dmaps []mongo.DASRecord, pkeys []string) {
	// defer function will propagate panic message to higher level
	defer utils.ErrPropagate("processLocalApis")

	for _, dmap := range dmaps {
		urn := dasmaps.GetString(dmap, "urn")
		system := dasmaps.GetString(dmap, "system")
		expire := dasmaps.GetInt(dmap, "expire")
		api := fmt.Sprintf("L_%s_%s", system, urn)
		if utils.VERBOSE {
			log.Println("DAS local API", api)
		}
		// we use reflection to look-up api from our services/localapis.go functions
		// for details on reflection see
		// http://stackoverflow.com/questions/12127585/go-lookup-function-by-name
		t := reflect.ValueOf(services.LocalAPIs{})              // type of LocalAPIs struct
		m := t.MethodByName(api)                                // associative function name for given api
		args := []reflect.Value{reflect.ValueOf(dasquery.Spec)} // list of function arguments
		vals := m.Call(args)[0]                                 // return value
		records := vals.Interface().([]mongo.DASRecord)         // cast reflect value to its type
		//         log.Println("### LOCAL APIS", urn, system, expire, dmap, api, m, len(records))

		records = services.AdjustRecords(dasquery, system, urn, records, expire, pkeys)

		// get DAS record and adjust its settings
		dasrecord := services.GetDASRecord(dasquery)
		dasstatus := fmt.Sprintf("process %s:%s", system, urn)
		dasexpire := services.GetExpire(dasrecord)
		if len(records) != 0 {
			rec := records[0]
			recexpire := services.GetExpire(rec)
			if dasexpire > recexpire {
				dasexpire = recexpire
			}
		}
		das := dasrecord["das"].(mongo.DASRecord)
		das["expire"] = dasexpire
		das["status"] = dasstatus
		dasrecord["das"] = das
		services.UpdateDASRecord(dasquery.Qhash, dasrecord)

		// fix all records expire values based on lowest one
		records = services.UpdateExpire(dasquery.Qhash, records, dasexpire)

		// insert records into DAS cache collection
		mongo.Insert("das", "cache", records)
	}
	// initial expire timestamp is 1h
	expire := utils.Expire(3600)
	// get DAS record and adjust its settings
	dasrecord := services.GetDASRecord(dasquery)
	dasexpire := services.GetExpire(dasrecord)
	if dasexpire < expire {
		dasexpire = expire
	}
	das := dasrecord["das"].(mongo.DASRecord)
	das["expire"] = dasexpire
	das["status"] = "ok"
	dasrecord["das"] = das
	services.UpdateDASRecord(dasquery.Qhash, dasrecord)

	// merge DAS cache records
	records, _ := services.MergeDASRecords(dasquery)
	mongo.Insert("das", "merge", records)
}

// helper function to process given set of URLs associted with dasquery
func processURLs(dasquery dasql.DASQuery, urls []string, maps []mongo.DASRecord, dmaps dasmaps.DASMaps, pkeys []string) {
	// defer function will propagate panic message to higher level
	defer utils.ErrPropagate("processUrls")

	out := make(chan utils.ResponseType)
	umap := map[string]int{}
	rmax := 3 // maximum number of retries
	for _, furl := range urls {
		umap[furl] = 0 // number of retries per url
		go utils.Fetch(furl, out)
	}

	// collect all results from out channel
	exit := false
	for {
		select {
		case r := <-out:
			if r.Error != nil {
				retry := umap[r.Url]
				if retry < rmax {
					retry += 1
					// incremenet sleep duration with every retry
					sleep := time.Duration(retry) * time.Second
					time.Sleep(sleep)
					umap[r.Url] = retry
				} else {
					delete(umap, r.Url) // remove Url from map
				}
			} else {
				system := ""
				//                 format := ""
				expire := 0
				urn := ""
				for _, dmap := range maps {
					surl := dasmaps.GetString(dmap, "url")
					// TMP fix, until we fix Phedex data to use JSON
					if strings.Contains(surl, "phedex") {
						surl = strings.Replace(surl, "xml", "json", -1)
					}
					if strings.Split(r.Url, "?")[0] == surl {
						urn = dasmaps.GetString(dmap, "urn")
						system = dasmaps.GetString(dmap, "system")
						expire = dasmaps.GetInt(dmap, "expire")
						//                         format = dasmaps.GetString(dmap, "format")
					}
				}
				// process data records
				notations := dmaps.FindNotations(system)
				records := services.Unmarshal(system, urn, r.Data, notations)
				records = services.AdjustRecords(dasquery, system, urn, records, expire, pkeys)

				// get DAS record and adjust its settings
				dasrecord := services.GetDASRecord(dasquery)
				dasstatus := fmt.Sprintf("process %s:%s", system, urn)
				dasexpire := services.GetExpire(dasrecord)
				if len(records) != 0 {
					rec := records[0]
					recexpire := services.GetExpire(rec)
					if dasexpire > recexpire {
						dasexpire = recexpire
					}
				}
				das := dasrecord["das"].(mongo.DASRecord)
				das["expire"] = dasexpire
				das["status"] = dasstatus
				dasrecord["das"] = das
				services.UpdateDASRecord(dasquery.Qhash, dasrecord)

				// fix all records expire values based on lowest one
				records = services.UpdateExpire(dasquery.Qhash, records, dasexpire)

				// insert records into DAS cache collection
				mongo.Insert("das", "cache", records)
				// remove from umap, indicate that we processed it
				delete(umap, r.Url) // remove Url from map
			}
		default:
			if len(umap) == 0 { // no more requests, merge data records
				records, expire := services.MergeDASRecords(dasquery)
				mongo.Insert("das", "merge", records)
				// get DAS record and adjust its settings
				dasrecord := services.GetDASRecord(dasquery)
				dasexpire := services.GetExpire(dasrecord)
				if dasexpire < expire {
					dasexpire = expire
				}
				das := dasrecord["das"].(mongo.DASRecord)
				das["expire"] = dasexpire
				das["status"] = "ok"
				dasrecord["das"] = das
				services.UpdateDASRecord(dasquery.Qhash, dasrecord)
				exit = true
			}
			time.Sleep(time.Duration(10) * time.Millisecond) // wait for response
		}
		if exit {
			break
		}
	}
}

// Process DAS query
func Process(dasquery dasql.DASQuery, dmaps dasmaps.DASMaps) string {
	// defer function will propagate panic message to higher level
	defer utils.ErrPropagate("Process")

	// find out list of APIs/CMS services which can process this query request
	maps := dmaps.FindServices(dasquery.Fields, dasquery.Spec)
	var urls, srvs, pkeys []string
	var local_apis []mongo.DASRecord
	// loop over services and fetch data
	for _, dmap := range maps {
		furl := formUrlCall(dasquery, dmap)
		if furl == "local_api" && !dasmaps.MapInList(dmap, local_apis) {
			local_apis = append(local_apis, dmap)
		} else if furl != "" && !utils.InList(furl, urls) {
			urls = append(urls, furl)
		}
		srv := fmt.Sprintf("%s:%s", dmap["system"], dmap["urn"])
		srvs = append(srvs, srv)
		lkeys := strings.Split(dmap["lookup"].(string), ",")
		for _, pkey := range lkeys {
			for _, item := range dmap["das_map"].([]interface{}) {
				rec := item.(mongo.DASRecord)
				daskey := rec["das_key"].(string)
				reckey := rec["rec_key"].(string)
				if daskey == pkey {
					pkeys = append(pkeys, reckey)
					break
				}
			}
		}
	}

	dasrecord := services.CreateDASRecord(dasquery, srvs, pkeys)
	var records []mongo.DASRecord
	records = append(records, dasrecord)
	mongo.Insert("das", "cache", records)

	// process local_api calls, we use GoFunc to run processLocalApis as goroutine in defer/silent mode
	// panic errors will be captured in GoFunc and passed again into this local function
	if len(local_apis) > 0 {
		utils.GoFunc("go processLocalApis", func() { processLocalApis(dasquery, local_apis, pkeys) })
	}
	// process URLs which will insert records into das cache and merge them into das merge collection
	if len(urls) > 0 {
		utils.GoFunc("go processURLs", func() { processURLs(dasquery, urls, maps, dmaps, pkeys) })
	}
	return dasquery.Qhash
}

// helper function to modify spec with given filter
func modSpec(spec bson.M, filter string) {
	var key, val string
	var vals []string
	if strings.Index(filter, "<") > 0 {
		if strings.Index(filter, "<=") > 0 {
			vals = strings.Split(filter, "<=")
		} else {
			vals = strings.Split(filter, "<")
		}
	} else if strings.Index(filter, "<") > 0 {
		if strings.Index(filter, ">=") > 0 {
			vals = strings.Split(filter, ">=")
		} else {
			vals = strings.Split(filter, ">")
		}
	} else if strings.Index(filter, "!=") > 0 {
		vals = strings.Split(filter, "!=")
	} else if strings.Index(filter, "=") > 0 {
		vals = strings.Split(filter, "=")
	} else {
		return
	}
	key = vals[0]
	val = vals[1]
	spec[key] = val
}

// Get data for given pid (DAS Query qhash)
func GetData(dasquery dasql.DASQuery, coll string, idx, limit int) (string, []mongo.DASRecord) {
	var empty_data, data []mongo.DASRecord
	pid := dasquery.Qhash
	filters := dasquery.Filters
	//     aggrs := dasquery.Aggregators
	spec := bson.M{"qhash": pid}
	skeys := filters["sort"]
	if len(filters) > 0 {
		var afilters []string
		for key, vals := range filters {
			if key == "grep" {
				for _, val := range vals {
					if strings.Index(val, "<") > 0 || strings.Index(val, "<") > 0 || strings.Index(val, "!") > 0 || strings.Index(val, "=") > 0 {
						modSpec(spec, val)
					} else {
						afilters = append(afilters, val)
					}
				}
			}
		}
		if len(afilters) > 0 {
			data = mongo.GetFilteredSorted("das", coll, spec, afilters, skeys, idx, limit)
		} else {
			data = mongo.Get("das", coll, spec, idx, limit)
		}
	} else {
		data = mongo.Get("das", coll, spec, idx, limit)
	}
	// Get DAS status from cache collection
	spec = bson.M{"qhash": pid, "das.record": 0}
	das_data := mongo.Get("das", "cache", spec, 0, 1)
	status, err := mongo.GetStringValue(das_data[0], "das.status")
	if err != nil {
		return fmt.Sprintf("failed to get data from DAS cache: %s\n", err), empty_data
	}
	if len(data) == 0 {
		return status, empty_data
	}
	return status, data
}

// Get number of records for given DAS query qhash
func Count(pid string) int {
	spec := bson.M{"qhash": pid}
	return mongo.Count("das", "merge", spec)
}

// Get initial timestamp of DAS query request
func GetTimestamp(pid string) int64 {
	spec := bson.M{"qhash": pid, "das.record": 0}
	data := mongo.Get("das", "cache", spec, 0, 1)
	ts, err := mongo.GetInt64Value(data[0], "das.ts")
	if err != nil {
		return time.Now().Unix()
	}
	return ts
}

// Check if data exists in DAS cache for given query/pid
// we look-up DAS record (record=0) with status ok (merging step is done)
func CheckDataReadiness(pid string) bool {
	espec := bson.M{"$gt": time.Now().Unix()}
	spec := bson.M{"qhash": pid, "das.expire": espec, "das.record": 0, "das.status": "ok"}
	nrec := mongo.Count("das", "cache", spec)
	if nrec == 1 {
		return true
	}
	return false
}

// Check if data exists in DAS cache for given query/pid
func CheckData(pid string) bool {
	espec := bson.M{"$gt": time.Now().Unix()}
	spec := bson.M{"qhash": pid, "das.expire": espec}
	nrec := mongo.Count("das", "cache", spec)
	if nrec > 0 {
		return true
	}
	return false
}

// Remove expired records
func RemoveExpired(pid string) {
	espec := bson.M{"$lt": time.Now().Unix()}
	spec := bson.M{"qhash": pid, "das.expire": espec}
	mongo.Remove("das", "cache", spec) // remove from cache collection
	mongo.Remove("das", "merge", spec) // remove from merge collection
}
