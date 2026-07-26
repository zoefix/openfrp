Median of 3 runs per measurement. Where repetitions disagreed by more than 15%, the observed range is shown beneath the median.

### Single-stream throughput

Higher is better. One connection, so there is no head-of-line blocking
to suffer; this isolates the cost of moving bytes through userspace
versus splicing them in the kernel.

| Scenario | OpenFrp | frp | Ratio |
|---|---:|---:|---:|
| LAN (no shaping) | 1191.13 MB/s<br><sub>1179.23–3808.36</sub> | 498.99 MB/s<br><sub>413.52–746.31</sub> | **2.39×** |
| 50 ms delay | 106.34 MB/s<br><sub>27.07–121.07</sub> | 102.3 MB/s | **1.04×** |
| 100 ms delay | 66.3 MB/s<br><sub>54.83–74.39</sub> | 56.71 MB/s<br><sub>34.35–58.19</sub> | **1.17×** |
| 200 ms delay | 35.91 MB/s | 27.94 MB/s | **1.29×** |
| 50 ms delay, 1% loss | 0.76 MB/s<br><sub>0.6–1.06</sub> | 0.76 MB/s | **1.00×** |
| 50 ms delay, 3% loss | 0.37 MB/s<br><sub>0.34–0.43</sub> | 0.41 MB/s<br><sub>0.34–0.47</sub> | 0.90× |

### Concurrent request latency

32 connections doing small round trips. Higher QPS and lower p99 are
better. This is where head-of-line blocking shows up: under loss a
multiplexed tunnel stalls every stream on one lost packet, while
independent connections stall only themselves.

| Scenario | OpenFrp QPS | frp QPS | Ratio | OpenFrp p99 | frp p99 |
|---|---:|---:|---:|---:|---:|
| LAN (no shaping) | 81264.9 | 55253<br><sub>44139.5–57778.5</sub> | **1.47×** | 0.839 ms | 1.268 ms<br><sub>1.164–2.298</sub> |
| 50 ms delay | 1055.9<br><sub>573.3–1113.1</sub> | 1123.2 | 0.94× | 37.837 ms<br><sub>31.149–125.403</sub> | 30.96 ms |
| 100 ms delay | 614.8 | 518.8<br><sub>334.1–620.7</sub> | **1.19×** | 55.832 ms | 140.259 ms<br><sub>52.923–142.851</sub> |
| 200 ms delay | 310.3 | 310.3<br><sub>233.5–310.4</sub> | **1.00×** | 106.314 ms | 105.985 ms<br><sub>105.892–200.539</sub> |
| 50 ms delay, 1% loss | 1088.1<br><sub>922.4–1097.5</sub> | 555.7<br><sub>344.2–596.6</sub> | **1.96×** | 129.097 ms<br><sub>31.075–254.082</sub> | 724.411 ms<br><sub>507.243–1072.96</sub> |
| 50 ms delay, 3% loss | 956.4 | 273.7<br><sub>175.2–323.1</sub> | **3.49×** | 258.55 ms | 609.691 ms<br><sub>560.685–674.552</sub> |
