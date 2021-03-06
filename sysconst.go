package test

// Misc system default configs
const (
	// default read 128KB
	DataBlockSize = 131072
	// ObjectMD data block split threshold, 4k blocks would be 4k*32bytes=128KB
	MaxDataBlocks = 4
	// 30s timeout for every single read/write operation
	RWTimeOutSecs = 30
	ZeroDataETag  = "d41d8cd98f00b204e9800998ecf8427e"
)

//const (
//  c0 = itoa // == 0
//  c1 // == 1
//)

// S3 related operation definitions
const (
	XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

	BucketListMaxKeys    = 1000
	BucketListOp         = "/?list-type=2"
	BucketAccelerate     = "/?accelerate"
	BucketCors           = "/?cors"
	BucketLifecycle      = "/?lifecycle"
	BucketPolicy         = "/?policy"
	BucketLogging        = "/?logging"
	BucketNotification   = "/?notification"
	BucketReplication    = "/?replication"
	BucketTag            = "/?tagging"
	BucketRequestPayment = "/?requestPayment"
	BucketVersioning     = "/?versioning"
	BucketWebsite        = "/?website"

	RequestID     = "x-request-id"
	ServerName    = "CloudZzzz"
	Server        = "Server"
	Date          = "Date"
	LastModified  = "Last-Modified"
	ETag          = "ETag"
	ContentLength = "Content-Length"
	ContentType   = "Content-Type"
)

// S3 error code
const (
	StatusOK                     = 200
	StatusOKStr                  = "OK"
	AccessDenied                 = 403
	BadDigest                    = 400
	BucketAlreadyExists          = 409
	BucketNotEmpty               = 409
	IncompleteBody               = 400
	InternalError                = 500
	InternalErrorStr             = "InternalError"
	InvalidArgument              = 400
	InvalidBucketName            = 400
	InvalidDigest                = 400
	InvalidLocationConstraint    = 400
	InvalidPart                  = 400
	InvalidPartOrder             = 400
	InvalidRange                 = 416
	InvalidRequest               = 400
	InvalidURI                   = 400
	KeyTooLong                   = 400
	MalformedACLError            = 400
	MalformedPOSTRequest         = 400
	MalformedXML                 = 400
	MaxMessageLengthExceeded     = 400
	MetadataTooLarge             = 400
	MethodNotAllowed             = 405
	MissingContentLength         = 411
	MissingRequestBodyError      = 400
	NoSuchBucket                 = 404
	NoSuchKey                    = 404
	NoSuchLifecycleConfiguration = 404
	NoSuchUpload                 = 404
	NotImplemented               = 501
	NotImplementedStr            = "NotImplemented"
	OperationAborted             = 409
	RequestTimeout               = 400
	RequestTimeTooSkewed         = 403
	SignatureDoesNotMatch        = 403
	ServiceUnavailable           = 503
	SlowDown                     = 503
	TokenRefreshRequired         = 400
)
