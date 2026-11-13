package verify

// ociFixtureDER is a throwaway 2048-bit RSA signing fixture in PKCS#1 DER
// form, base64 encoded. It was generated for these tests and was never
// registered with any service. It is fixed, not generated at run time,
// because the expected signature below was produced for it by the OCI SDK.
const ociFixtureDER = "MIIEowIBAAKCAQEAzdV+pWxIDlumG4FbPRYHiE32sN+ygkIT4/mIVI+/9LZFg/FAU9t1Wg2d" +
	"8Cx9V3JnHd/6m4GYoTR/BTQKKg+eEzXtkTMMeEO2py0OzftaTYykXvsU4LOStPy6OZ+yHXbG" +
	"TF+gvsONG1BpuZiDyPLQVXmYLrwHdXaJDZb+VEcacLDCdCHjE9mZgIe5SXRVAVpvJ6omPy4p" +
	"5VBUlRmDbAMIcuoi3pD8O/NDWXyYw679XbYUWFiSdeOHuX8HYrc5+b55JEtYI7BHqz5W3vEq" +
	"UVpLDtdnXLycr8gwAcvNgkNnjC8JNCs7KuxOdk21zPnPJ1fjDZtmGelNV/6vJ7IQRLOTwQID" +
	"AQABAoIBAEfHOaDqP75yC1zwIScONZY8ydgoShHOhS0s6XPDpUbrARbf3HThxOygbwFAIn2K" +
	"MZdjcfskmFsu+VSRLzI3vAIq/ityR9jwlBN8TPtbucPtVayhlkg9lt1yfGZ34B8SOliLyHx9" +
	"qQlrY1MLcnly41/LRVyWyWsW6PhKPWqaWQgOm47Aql8W8Nbr6TcBNg67H2AhlWfEGexxBf04" +
	"hy0MuOA02muVogDI2WbwQj1mU2zJN98Dcy/Fa7dAcSgKhZYT/xMovPYZ0BimPHo/3sddaNTB" +
	"GWojYzKKMvVTkC+2yfQceaZy/4/O1zlWlGp7s65IdhBqaufMqjKELnyTBFg2/5kCgYEA8fCQ" +
	"xtC8clb1GfYmnArpILVxMPG0MMFLRJ54+ax2/hAGkAcmWdXQLGp6oTOQ9TH/49t8SDy97tzE" +
	"mBeoPo+DEG57ZdhHObwuLlQ3ecKdmmk/1ML7y20tSlmi6vCZ8Vk4II+jr4itbDAdsQ6kvbMP" +
	"wrdrNsNPrQp46QXw3kbTpJcCgYEA2cvE+7xSftl+5/fk7nCA9wmoFSrHlLlZHSfz8LevSA0z" +
	"QRwxgtnLbJr6P0dvdTdr2T9fJOLSLH2xT+ashlRqiD2k7mMU7bxFiEPLFdm304yL1OCgp283" +
	"Y2NmGIBVXUEeUYYIGB1NHuUYRiYI6xPyaIJziUMGTbY69XYKZfP63WcCgYAL31I7b2LP+ZRt" +
	"gbbDUYW/JSg4zqOZAONAqsYk+mGcdgzcZH1tSyC8DT2XS4CBh8KW295tbQ2k+xBqpj6LU1+L" +
	"HuDPTp2wJ5vciiVuxC4XtbJ8LlVM1j6yt1NXVBc50LcAYgwWQQ7BkFrkpWNQK8lKNNX+CfeO" +
	"9kSYqsH3JmKcYQKBgQDW7ZHs4sF+UOA9xpJzAy8hmrP2f4sJJ4fbj+vZZ0tgjTZBULNDb+6h" +
	"tKaz/6jsQc76SuF3oybJpRhvwzIKNhATdtHmNn0Wxq7PdCdb3sjbyVzMrQedc9FzEUpVb3/O" +
	"UU68IMqoEXJjxK0ZvLscUUjs/Z9Km4wC8IxqYxyL1COCjQKBgF4e+Vb3kQS6wrD3sWkXP1Zj" +
	"rmITu5O4es2Wv9AbgebGQ4R8rY9E53k+G8+lvDrVPQlz+tZSFeHJ++FnP2aQWVhof3+2ofuZ" +
	"9coI7MDJygH7KfZoUsjJZY7HDtvstb720LuEYCaA9H0d0EWaKLLqQBbdFKS1rNiJA9am5jNs" +
	"W7Sn"

// ociFixtureFingerprint is the value of
// `openssl rsa -pubout -outform DER | openssl md5 -c` for the fixture.
const ociFixtureFingerprint = "f3:90:fa:0a:5c:59:ff:7d:2f:ad:77:ad:7f:fd:b6:42"

// ociSDKSignature is the signature that oci-go-sdk v65.130.0
// (common.DefaultRequestSigner) put in the Authorization header for the
// fixture, the request in TestOCISign_MatchesSDKSigner, and the date
// Thu, 12 Nov 2026 08:30:00 GMT.
const ociSDKSignature = "xhTQhlewFoiGcJPdjjpK7B9yRVO65cGFu0l7ctHsgYN4s/B3NAyM97pQtd25+xmYgTrWSV12" +
	"Vcbu4GjrSMAKPYlSoneksJAY8PX1KShQOCmYlfqhrJQb6BBPpAXlU+jFJvxvO+GAmXkxRp0M" +
	"gJ5J2O/XSO7lV5qu9Qz5PgDocw5valdygXpDZu89elFQ4ijudBpC/J3zO0HewgZmIeXs/Z0x" +
	"FhWD279rqat9a8+PHA2+DKeb/N0rJ2pPTfFmIavAXqvDRDS0xvXh1dwCWu7y/VIxkdd0FjcZ" +
	"Sq8t5opl+MhbaQBpX+CFAjqomrsU9lZb5hv9UIU0Ns/059AxgjmYjw=="
