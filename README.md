# ethlb

A chain-aware load balancer for EVM-compatible cryptocurrency nodes. An Open-Source implementation of the concepts behind [Alchemy's SuperNode](https://www.alchemy.com/supernode) to enable at-scale crypto platforms to support millions of concurrent sessions while ensuring high performance and consistently accurate data.

## Features

### Chain Aware Node Load Balancing

Conventional TCP/UDP load balancers simply distribute load across N number of upstream origins, possibly with some client sticky session logic. ethlb is chain-aware, which means that it will only ever route traffic to node(s) which have the latest blocks as requested by the client. Nodes which fall behind are temporarily removed from the pool to allow them to catch up to head before being re-added to the pool.

### Increased Reliability

As ethlb distributes load across multiple nodes, downstream services are not dependent on any one blockchain node. This allows nodes to be deployed across failure domains and/or geographically dispersed for high availability.

### Increased Performance

In addition to load balancing requests, ethlb also caches responses to increase performance of subsequent data retrieval requests. Common data such as block headers, transaction receipts, and contract logs are cached in Redis to support high scale data ingestion and analytics workloads without affecting end-user performance.

### Scalability

ethlb is designed to be scalable to support thousands of concurrent sessions while maintaining high performance and consistently accurate data. As ethlb supports distributed crypto architectures, platforms can scale to thousands of nodes without requiring any additional infrastructure.

### Proactive Health Checks

ethlb continually probes upstream nodes for health and accuracy, and will remove nodes from the pool if they are deemed unhealthy. Rather than passive monitoring of in-flight requests, ethlb will actively perform out-of-band health checks to ensure that nodes are always available for load balancing.

### First Class Metrics

ethlb exposes metrics for monitoring and tuning performance in Prometheus format.