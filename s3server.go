package test

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"flag"
	"hash"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/golang/protobuf/proto"
)

var ioengine = flag.String("io", "fileio", "the cloud ioengine: fileio or cloudio")

// S3Server handles the coming S3 requests
type S3Server struct {
	s3io CloudIO
}

// NewS3Server allocates a new S3Server instance
func NewS3Server() *S3Server {
	s := new(S3Server)
	if *ioengine == "fileio" {
		fio := NewFileIO()
		if fio == nil {
			glog.Errorln("failed to create CloudIO instance, type", *ioengine)
			return nil
		}
		s.s3io = fio
	}

	glog.Infoln("created S3Server, type", *ioengine)
	return s
}

func (s *S3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := s.newResponse()
	bkname, objname := s.getBucketAndObjectName(r)

	glog.V(2).Infoln(r.Method, r.URL, r.Host, bkname, objname)

	if bkname == "" {
		glog.Errorln("InvalidRequest, no bucketname", r.Method, r.URL, r.Host)
		resp.StatusCode = InvalidRequest
		resp.Status = "InvalidRequest, no bucketname"
		resp.Write(w)
		return
	}

	switch r.Method {
	case "POST":
	case "PUT":
		s.putOp(r, resp, bkname, objname)
	case "GET":
		s.getOp(r, resp, bkname, objname)
	case "HEAD":
		s.headOp(r, resp, bkname, objname)
	case "DELETE":
		s.delOp(r, resp, bkname, objname)
	case "OPTIONS":
	default:
		glog.Errorln("unsupported request", r.Method, r.URL)
		resp.StatusCode = InvalidRequest
	}

	resp.Header.Set(Date, time.Now().Format(time.RFC1123))

	err := resp.Write(w)
	if err != nil {
		glog.Errorln("failed to write resp", r.Method, bkname, objname, err)
		resp.Body.Close()
	}
}

func (s *S3Server) newResponse() *http.Response {
	resp := new(http.Response)
	resp.ProtoMajor = 1
	resp.ProtoMinor = 1
	resp.StatusCode = InvalidRequest
	resp.Status = "not support request"
	resp.Header = make(http.Header)
	resp.Header.Set(Server, ServerName)
	return resp
}

// S3 supports 2 types url.
// virtual-hosted–style: http://bucket.s3-aws-region.amazonaws.com
// path-style: http://s3-aws-region.amazonaws.com/bucket
func (s *S3Server) getBucketFromHost(host string) (bkname string) {
	urls := strings.Split(host, ".")

	if len(urls) == 3 {
		// path-style url
		return ""
	}

	if len(urls) == 4 {
		// check ip address or virtual-hosted-style url
		if strings.HasPrefix(urls[1], "s3") {
			// ip address
			return urls[0]
		}

		return ""
	}

	// TODO invalid URL?
	return ""
}

// get bucket and object name from request
func (s *S3Server) getBucketAndObjectName(r *http.Request) (bkname string, objname string) {
	bkname = s.getBucketFromHost(r.Host)

	if bkname == "" {
		// path-style url, get bucket name and object name from URL
		// url like /b1/k1 will be split to 3 elements, [ b1 k1].
		// /b1/ also 3 elements [ b1 ].
		// /b1 2 elements [ b1].
		strs := strings.SplitN(r.URL.String(), "/", 3)
		l := len(strs)
		if l == 3 {
			return strs[1], "/" + strs[2]
		} else if l == 2 {
			return strs[1], "/"
		} else {
			return "", ""
		}
	} else {
		// bucket is in r.Host, the whole URL is object name
		return bkname, r.URL.String()
	}
}

func (s *S3Server) isBucketOp(objname string) bool {
	if objname == "" || objname == "/" ||
		strings.HasPrefix(objname, "/?") || strings.HasPrefix(objname, "?") {
		return true
	}
	return false
}

func (s *S3Server) putOp(r *http.Request, resp *http.Response, bkname string, objname string) {
	if s.isBucketOp(objname) {
		if objname == "" || objname == "/" {
			resp.StatusCode, resp.Status = s.s3io.PutBucket(bkname)
			glog.Infoln("put bucket", r.URL, r.Host, bkname, resp.StatusCode, resp.Status)
		} else {
			glog.Errorln("not support put bucket operation", bkname, objname)
		}
	} else {
		s.putObject(r, resp, bkname, objname)
	}
}

func (s *S3Server) readFullBuf(r *http.Request, readBuf []byte) (n int, err error) {
	readZero := false
	var rlen int
	for rlen < len(readBuf) {
		n, err = r.Body.Read(readBuf[rlen:])
		rlen += n

		if err != nil {
			return rlen, err
		}

		if n == 0 && err == nil {
			if readZero {
				glog.Errorln("read 0 bytes from http with nil error twice", r.URL, r.Host)
				return rlen, err
			}
			// allow read 0 byte with nil error once
			readZero = true
		}
	}
	return rlen, err
}

// if object data < ReadBufferSize
func (s *S3Server) putSmallObjectData(r *http.Request, md *ObjectMD) (status int, errmsg string) {
	readBuf := make([]byte, r.ContentLength)

	// read all data
	n, err := s.readFullBuf(r, readBuf)
	glog.V(4).Infoln("read", n, err, "ContentLength",
		r.ContentLength, md.Smd.Bucket, md.Smd.Name)

	if err != nil {
		if err != io.EOF {
			glog.Errorln("failed to read data from http", err, "ContentLength",
				r.ContentLength, md.Smd.Bucket, md.Smd.Name)
			return InternalError, "failed to read data from http"
		}

		// EOF, check if all contents are readed
		if int64(n) != r.ContentLength {
			glog.Errorln("read", n, "less than ContentLength",
				r.ContentLength, md.Smd.Bucket, md.Smd.Name)
			return InvalidRequest, "data less than ContentLength"
		}
	}

	// compute checksum
	m := md5.New()
	m.Write(readBuf)
	md5byte := m.Sum(nil)
	md5str := hex.EncodeToString(md5byte)
	m.Reset()

	// write data block
	if !s.s3io.IsDataBlockExist(md5str) {
		status, errmsg = s.s3io.WriteDataBlock(readBuf, md5str)
		if status != StatusOK {
			glog.Errorln("failed to create data block",
				md5str, status, errmsg, md.Smd.Bucket, md.Smd.Name)
			return status, errmsg
		}
		glog.V(2).Infoln("create data block", md5str, r.ContentLength)
	} else {
		md.Data.DdBlocks = 1
		glog.V(2).Infoln("data block exists", md5str, r.ContentLength)
	}

	// add to data block
	md.Data.BlockSize = ReadBufferSize
	md.Data.Blocks = append(md.Data.Blocks, md5str)

	// set etag
	md.Smd.Etag = md5str
	return StatusOK, StatusOKStr
}

type writeDataBlockResult struct {
	md5str string // data block md5
	exist  bool   // whether data block exists
	status int
	errmsg string
}

func (s *S3Server) writeOneDataBlock(buf []byte, md5ck hash.Hash, etag hash.Hash,
	md *ObjectMD, c chan<- writeDataBlockResult, quit <-chan bool) {
	// compute checksum
	md5ck.Write(buf)
	md5byte := md5ck.Sum(nil)
	md5str := hex.EncodeToString(md5byte)
	// reset md5 for the next block
	md5ck.Reset()

	// update etag
	etag.Write(buf)

	res := writeDataBlockResult{md5str, true, StatusOK, StatusOKStr}

	// write data block
	if !s.s3io.IsDataBlockExist(md5str) {
		res.exist = false
		res.status, res.errmsg = s.s3io.WriteDataBlock(buf, md5str)
		glog.V(2).Infoln("create data block", md5str, res.status, len(buf))
	} else {
		glog.V(2).Infoln("data block exists", md5str, len(buf))
	}

	select {
	case c <- res:
		glog.V(5).Infoln("sent writeDataBlockResult", md5str, md.Smd)
	case <-quit:
		glog.V(5).Infoln("write data block quit", md.Smd)
	case <-time.After(RWTimeOutSecs * time.Second):
		glog.Errorln("write data block timeout", md5str, md.Smd,
			"NumGoroutine", runtime.NumGoroutine())
	}
}

// read object data and create data blocks.
// this func will update data blocks and etag in ObjectMD
func (s *S3Server) putObjectData(r *http.Request, md *ObjectMD) (status int, errmsg string) {
	if r.ContentLength <= ReadBufferSize {
		return s.putSmallObjectData(r, md)
	}

	readBuf := make([]byte, ReadBufferSize)
	writeBuf := make([]byte, ReadBufferSize)

	md5ck := md5.New()
	etag := md5.New()

	var totalBlocks int64
	// minimal dd blocks
	var ddBlocks int64

	// chan to wait till the previous write completes
	c := make(chan writeDataBlockResult)
	quit := make(chan bool)
	waitWrite := false

	var rlen int64
	for rlen < r.ContentLength {
		// read one block
		n, err := s.readFullBuf(r, readBuf)
		rlen += int64(n)
		glog.V(4).Infoln("read", n, err, "total readed len", rlen,
			"specified read len", r.ContentLength, md.Smd.Bucket, md.Smd.Name)

		if RandomFI() {
			glog.Errorln("FI at putObjectData", rlen, r.ContentLength, md.Smd,
				"NumGoroutine", runtime.NumGoroutine())
			if RandomFI() {
				// test writer timeout to exit goroutine
				return InternalError, "exit early to test chan timeout"
			}
			// test quit writer
			err = io.EOF
		}

		if err != nil {
			if err != io.EOF {
				glog.Errorln("failed to read data from http", err, "readed len", rlen,
					"ContentLength", r.ContentLength, md.Smd.Bucket, md.Smd.Name)
				if waitWrite {
					glog.V(5).Infoln("notify writer to quit", md.Smd)
					quit <- true
				}
				return InternalError, "failed to read data from http"
			}

			// EOF, check if all contents are readed
			if rlen != r.ContentLength {
				glog.Errorln("read", rlen, "less than ContentLength",
					r.ContentLength, md.Smd.Bucket, md.Smd.Name)
				if waitWrite {
					glog.V(5).Infoln("notify writer to quit", md.Smd)
					quit <- true
				}
				return InvalidRequest, "data less than ContentLength"
			}

			// EOF, check if the last data block is 0
			if n == 0 {
				break // break the for loop
			}

			// write out the last data block
		}

		if waitWrite {
			// wait data block write done
			res := <-c

			if res.status != StatusOK {
				glog.Errorln("failed to create data block", res.md5str,
					res.status, res.errmsg, md.Smd.Bucket, md.Smd.Name)
				return res.status, res.errmsg
			}

			if res.exist {
				ddBlocks++
			}
			totalBlocks++
			// add to data block
			md.Data.Blocks = append(md.Data.Blocks, res.md5str)
		}

		// write data block
		// switch buffer, readBuf will be used to read the next data block
		tmpbuf := readBuf
		readBuf = writeBuf
		writeBuf = tmpbuf
		waitWrite = true
		// Note: should we switch to a single routine, which loops to write data
		// block. and here invoke the routine via chan? assume go internally has
		// like a queue for all routines, and one thread per core to schedule them.
		// Sounds no big difference? an old routine + chan vs a new routine.
		go s.writeOneDataBlock(writeBuf[:n], md5ck, etag, md, c, quit)
	}

	// wait the last write
	if waitWrite {
		// wait data block write done
		res := <-c

		if res.status != StatusOK {
			glog.Errorln("failed to create data block", res.md5str,
				res.status, res.errmsg, md.Smd.Bucket, md.Smd.Name)
			return res.status, res.errmsg
		}

		if res.exist {
			ddBlocks++
		}
		totalBlocks++
		// add to data block
		md.Data.Blocks = append(md.Data.Blocks, res.md5str)
	}

	glog.V(1).Infoln(md.Smd.Bucket, md.Smd.Name, r.ContentLength,
		"totalBlocks", totalBlocks, "ddBlocks", ddBlocks)

	etagbyte := etag.Sum(nil)
	md.Smd.Etag = hex.EncodeToString(etagbyte)
	md.Data.DdBlocks = ddBlocks
	return StatusOK, StatusOKStr
}

func (s *S3Server) putObject(r *http.Request, resp *http.Response, bkname string, objname string) {
	// Performance is one critical factor for this dedup layer. Not doing the
	// additional operations here, such as bucket permission check, etc.
	// When creating the metadata object, S3 will do all the checks. If S3
	// rejects the request, no positive refs will be added for the data blocks.
	// gc will clean up them in the background.

	// create the metadata object
	smd := &ObjectSMD{}
	smd.Bucket = bkname
	smd.Name = objname
	smd.Mtime = time.Now().Unix()
	smd.Size = r.ContentLength

	data := &DataBlock{}
	data.BlockSize = ReadBufferSize

	md := &ObjectMD{}
	md.Smd = smd
	md.Data = data

	// read object data and create data blocks
	status, errmsg := s.putObjectData(r, md)
	if status != StatusOK {
		resp.StatusCode = status
		resp.Status = errmsg
		return
	}

	// Marshal ObjectMD to []byte
	mdbyte, err := proto.Marshal(md)
	if err != nil {
		glog.Errorln("failed to Marshal ObjectMD", md, err)
		resp.StatusCode = InternalError
		resp.Status = "failed to Marshal ObjectMD"
		return
	}

	// write out ObjectMD
	status, errmsg = s.s3io.WriteObjectMD(bkname, objname, mdbyte)
	if status != StatusOK {
		resp.StatusCode = status
		resp.Status = errmsg
		return
	}

	glog.V(0).Infoln("successfully created object", bkname, objname, md.Smd.Etag)

	resp.Header.Set(ETag, md.Smd.Etag)
	resp.StatusCode = StatusOK
	resp.Status = StatusOKStr
}

type dataBlockReadResult struct {
	blkIdx int
	buf    []byte
	n      int
	status int
	errmsg string
}

type objectDataIOReader struct {
	resp  *http.Response
	s3io  CloudIO
	objmd *ObjectMD
	off   int64
	// the current cached data block
	currBlock dataBlockReadResult
	// whether needs to wait for the outgoing prefetch
	waitBlock bool
	// channel to wait till the background prefetch complete
	c chan dataBlockReadResult
	// the reader is possible to involve 3 threads, th1 may be prefetching the block,
	// th2 may be at any step of Read(), th3 may call Close() at any time.
	// channel to handle close on resp http exception
	closed chan bool
	// internal for FI usage only, to avoid close the chan multiple times
	closeCalled bool
}

func (d *objectDataIOReader) readBlock(blk int, b []byte) dataBlockReadResult {
	res := dataBlockReadResult{blkIdx: blk, buf: b}

	// sanity check
	if blk >= len(d.objmd.Data.Blocks) {
		glog.Errorln("no more block to read", blk, d.objmd)
		res.status = InternalError
		res.errmsg = "no more block to read"
		return res
	}

	res.n, res.status, res.errmsg =
		d.s3io.ReadDataBlockRange(d.objmd.Data.Blocks[blk], 0, res.buf)

	glog.V(2).Infoln("read block done", blk, d.objmd.Data.Blocks[blk],
		res.n, res.status, res.errmsg, d.objmd.Smd)

	if res.status == StatusOK && res.n != int(d.objmd.Data.BlockSize) &&
		res.blkIdx != len(d.objmd.Data.Blocks)-1 {
		// read less data, could only happen for the last block
		glog.Errorln("not read full block", res.n, blk, d.objmd)
		res.status = InternalError
		res.errmsg = "read less data for a full block"
	}

	return res
}

func (d *objectDataIOReader) prefetchBlock(blk int, b []byte) {
	glog.V(5).Infoln("prefetchBlock start", blk, d.objmd.Smd)

	if RandomFI() {
		// TODO how to let client receives error?
		// simulate the connection broken and Close() is called
		glog.Errorln("FI at prefetchBlock, close d.closed chan", blk, d.objmd.Smd)
		d.closeChan()
	}

	res := d.readBlock(blk, b)
	select {
	case d.c <- res:
		glog.V(5).Infoln("prefetchBlock sent res to chan done", blk, d.objmd.Smd)
	case <-d.closed:
		glog.Errorln("stop prefetchBlock, reader closed", blk, d.objmd.Smd)
	case <-time.After(RWTimeOutSecs * time.Second):
		glog.Errorln("stop prefetchBlock, timeout", blk, d.objmd.Smd)
	}
}

func (d *objectDataIOReader) closeChan() {
	if FIEnabled() && d.closeCalled {
		return
	}

	glog.V(5).Infoln("closeChan", d.off, d.objmd.Smd.Size)

	// close the "closed" channel, so both prefetchBlock() and Read() can exit
	close(d.closed)
	d.closeCalled = true
}

func (d *objectDataIOReader) Close() error {
	glog.V(2).Infoln("objectDataIOReader close called", d.off, d.objmd.Smd.Size)
	d.closeChan()
	return nil
}

func (d *objectDataIOReader) Read(p []byte) (n int, err error) {
	if d.off >= d.objmd.Smd.Size {
		glog.V(1).Infoln("finish read object data", d.objmd.Smd)
		return 0, io.EOF
	}

	// TODO setting resp.StatusCode here looks useless?

	// compute the corresponding data block and offset inside data block
	idx := int(d.off / int64(d.objmd.Data.BlockSize))
	blockOff := int(d.off % int64(d.objmd.Data.BlockSize))

	if idx < d.currBlock.blkIdx {
		// sanity check, this should not happen
		glog.Errorln("read the previous data again?",
			d.off, idx, d.currBlock.blkIdx, d.objmd.Smd)
		d.resp.StatusCode = InvalidRequest
		d.resp.Status = "read previous data again"
		return 0, errors.New("Invalid read request, read previous data again")
	}

	if idx > d.currBlock.blkIdx {
		// sanity check, the prefetch task should be sent already
		if !d.waitBlock {
			glog.Errorln("no prefetch task", idx, d.off, d.objmd.Smd)
			d.resp.StatusCode = InternalError
			d.resp.Status = "no prefetch task"
			return 0, errors.New("InternalError, no prefetch task")
		}

		glog.V(5).Infoln("wait the prefetch block", idx, d.off, d.objmd.Smd)

		if RandomFI() {
			// simulate the connection broken and Close() is called
			// Q: looks the ongoing Read still goes through, d.closed looks not used here.
			glog.Errorln("FI at Read, close d.closed chan", idx, d.off, d.objmd.Smd)
			d.closeChan()
		}

		// current block is read out, wait for the next block
		select {
		case nextBlock := <-d.c:
			d.waitBlock = false

			glog.V(5).Infoln("get the prefetch block", idx, d.off, d.objmd.Smd)

			// the next block is back, switch the current block to the next block
			oldbuf := d.currBlock.buf
			d.currBlock = nextBlock

			// prefetch the next block if necessary
			if d.currBlock.status == StatusOK && d.off+int64(d.currBlock.n) < d.objmd.Smd.Size {
				d.waitBlock = true
				go d.prefetchBlock(d.currBlock.blkIdx+1, oldbuf)
			}
		case <-d.closed:
			glog.Errorln("stop Read, reader closed", idx, d.off, d.objmd.Smd)
			d.resp.StatusCode = InternalError
			d.resp.Status = "connection closed prematurely"
			return 0, errors.New("connection closed prematurely")
		case <-time.After(RWTimeOutSecs * time.Second):
			glog.Errorln("stop Read, timeout", idx, d.off, d.objmd.Smd)
			d.resp.StatusCode = InternalError
			d.resp.Status = "internal read timeout"
			return 0, errors.New("read timeout")
		}
	}

	// check the current block read status
	if d.currBlock.status != StatusOK {
		glog.Errorln("read data block failed", idx, d.objmd.Data.Blocks[idx],
			d.off, d.currBlock.status, d.currBlock.errmsg, d.objmd.Smd)
		d.resp.StatusCode = d.currBlock.status
		d.resp.Status = d.currBlock.errmsg
		return 0, errors.New(d.currBlock.errmsg)
	}

	// fill data from the current block
	glog.V(2).Infoln("fill data from currBlock",
		idx, blockOff, d.off, d.currBlock.n, d.objmd.Smd)

	endOff := blockOff + len(p)
	if endOff <= d.currBlock.n {
		// currBlock has more data than p
		glog.V(5).Infoln("currBlock has enough data",
			idx, blockOff, endOff, d.off, d.currBlock.n, d.objmd.Smd)

		copy(p, d.currBlock.buf[blockOff:endOff])
		n = len(p)
	} else {
		// p could have more data than the rest in currBlock
		// TODO copy the rest data from the next block
		glog.V(5).Infoln("read the end of currBlock",
			idx, blockOff, endOff, d.off, d.currBlock.n, d.objmd.Smd)

		copy(p, d.currBlock.buf[blockOff:d.currBlock.n])
		n = d.currBlock.n - blockOff
	}

	d.off += int64(n)

	if d.off == d.objmd.Smd.Size {
		return n, io.EOF
	}

	return n, nil
}

func (s *S3Server) getOp(r *http.Request, resp *http.Response, bkname string, objname string) {
	if s.isBucketOp(objname) {
		if objname == "" || objname == "/" || objname == BucketListOp {
			s.s3io.GetBucket(bkname, resp)
		} else {
			glog.Errorln("not support get bucket operation", bkname, objname)
		}
	} else {
		s.getObjectOp(r, resp, bkname, objname)
	}
}

func (s *S3Server) getObjectMD(bkname string, objname string) (objmd *ObjectMD, status int, errmsg string) {
	// object get, read metadata object first
	b, status, errmsg := s.s3io.ReadObjectMD(bkname, objname)
	if status != StatusOK {
		glog.Errorln("failed to ReadObjectMD", bkname, objname, status, errmsg)
		return nil, status, errmsg
	}

	objmd = &ObjectMD{}
	err := proto.Unmarshal(b, objmd)
	if err != nil {
		glog.Errorln("failed to Unmarshal ObjectMD", bkname, objname, err)
		return nil, InternalError, InternalErrorStr
	}

	glog.V(2).Infoln("successfully read object md", bkname, objname)
	return objmd, StatusOK, StatusOKStr
}

func (s *S3Server) getObjectOp(r *http.Request, resp *http.Response, bkname string, objname string) {
	// object get, read metadata object
	var objmd *ObjectMD
	objmd, resp.StatusCode, resp.Status = s.getObjectMD(bkname, objname)
	if resp.StatusCode != StatusOK {
		glog.Errorln("getObjecct failed to get ObjectMD",
			bkname, objname, resp.StatusCode, resp.Status)
		return
	}

	resp.ContentLength = objmd.Smd.Size

	if objmd.Smd.Size == 0 {
		glog.V(1).Infoln("successfully read 0 size object", bkname, objname)
		return
	}

	// construct Body reader
	// read the corresponding data blocks
	rd := new(objectDataIOReader)
	rd.resp = resp
	rd.s3io = s.s3io
	rd.objmd = objmd
	rd.closed = make(chan bool)

	// synchronously read the first block
	b := make([]byte, objmd.Data.BlockSize)
	rd.currBlock = rd.readBlock(0, b)

	// check the first block read status
	if rd.currBlock.status != StatusOK {
		glog.Errorln("read first data block failed", objmd.Data.Blocks[0],
			rd.currBlock.status, rd.currBlock.errmsg, bkname, objname)
		resp.StatusCode = rd.currBlock.status
		resp.Status = rd.currBlock.errmsg
		return
	}

	// if there are more data to read, start the prefetch task
	if objmd.Smd.Size > int64(objmd.Data.BlockSize) {
		rd.c = make(chan dataBlockReadResult)
		nextbuf := make([]byte, objmd.Data.BlockSize)
		rd.waitBlock = true
		go rd.prefetchBlock(1, nextbuf)
	}

	resp.Body = rd
}

func (s *S3Server) delOp(r *http.Request, resp *http.Response, bkname string, objname string) {
	if s.isBucketOp(objname) {
		if objname == "" || objname == "/" {
			resp.StatusCode, resp.Status = s.s3io.DeleteBucket(bkname)
			glog.Infoln("del bucket", r.URL, r.Host, bkname, resp.StatusCode, resp.Status)
		} else {
			glog.Errorln("not support delete bucket operation", bkname, objname)
		}
	} else {
		s.putObject(r, resp, bkname, objname)
	}
}

func (s *S3Server) headOp(r *http.Request, resp *http.Response, bkname string, objname string) {
	if s.isBucketOp(objname) {
		if objname == "" || objname == "/" {
			resp.StatusCode, resp.Status = s.s3io.HeadBucket(bkname)
			glog.V(2).Infoln("head bucket", r.URL, r.Host, bkname, resp.StatusCode, resp.Status)
		} else {
			glog.Errorln("invalid head bucket operation", r.URL, r.Host, bkname)
			resp.Status = "invalid head bucket operation"
		}
	} else {
		s.headObject(r, resp, bkname, objname)
	}
}

func (s *S3Server) headObject(r *http.Request, resp *http.Response, bkname string, objname string) {
	// get ObjectMD
	var objmd *ObjectMD
	objmd, resp.StatusCode, resp.Status = s.getObjectMD(bkname, objname)
	if resp.StatusCode != StatusOK {
		glog.Errorln("headObjecct failed to get ObjectMD",
			bkname, objname, resp.StatusCode, resp.Status)
		return
	}

	glog.V(2).Infoln("headObject", objmd.Smd)

	// fill the response
	resp.Header.Set(LastModified, time.Unix(objmd.Smd.Mtime, 0).Format(time.RFC1123))
	resp.Header.Set(ETag, objmd.Smd.Etag)
	resp.ContentLength = objmd.Smd.Size
}
