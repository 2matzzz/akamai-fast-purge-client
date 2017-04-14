Akamai Fast Purge Client
========================

Requirement
-----------

* golang
* akamai edgegrid credential

How to use
----------

At first, you can set up your environment to use [Akamai Open API](https://developer.akamai.com/api).

See also: [https://developer.akamai.com/introduction/Setup_Environment.html](https://developer.akamai.com/introduction/Setup_Environment.html)

Next, build the application as below.

```
make build
```

Finally, you can purge the cache.

```
bin/akamai-fast-purge-client_YOUROS_YOURARCH sample/invalidation-request-body
```

Help
----

```
bin/akamai-fast-purge-client_YOUROS_YOURARCH -h
```