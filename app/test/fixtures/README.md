Self-signed test certificates (EC P-256, valid 100 years, CN `agentd-test-a|b`, SAN localhost/127.0.0.1)
used by `test/api/pin_test.dart`. `*.pin` is the `sha256:<hex>` SPKI pin computed with openssl:

    openssl x509 -in a.cert.pem -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -hex

The keys protect nothing: they exist only for the local TLS stub server of the tests.
