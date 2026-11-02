package accountapi

// Keys made with ssh-keygen, from the tests of internal/sshkeys: one that is fine and one that is too short to be accepted.
const (
	ed25519Line = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBhgpejLQl2Ae5qAX7iRAbunnQxYZbuWBxF3zxC/xher test-ed"
	rsa1024Line = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDlNdofERg8ADqeMuxkd1Jzi0SjXoq5KgR3GKpr2ycnTZOu4vd3XMR4gWbSRptj7VYS+Y9XrGcLYRnnGoPavhQ7G5GE9tEu6whnw5a9u82ESRzeAr8Fq9hgRn9a/i6nR1QxvUnEIxkJcFkFhB5lScWZ2mEFUVDtKdT4Tb99SRtVcQ== test-rsa1024"
)

// ed25519Fingerprint is what `ssh-keygen -l` printed for ed25519Line, without the "SHA256:".
const ed25519Fingerprint = "/jWxgXv75WMhCGdCuGjOluuSlDlO1hmb5tYUR/rDQ1U"
