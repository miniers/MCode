package main

// waitOpenWithLoading 在后台等 app.Open 的结果或整理数据库的信号：结果先到就直接交给 done；
// 整理信号先到就调用一次 show，拿到结果后再调用 done。
// show 与 done 的执行线程由调用方负责（生产代码里都转到主线程），这里只保证调用次数与顺序。
func waitOpenWithLoading(result <-chan openResult, loading <-chan struct{}, show func(), done func(openResult)) {
	go func() {
		select {
		case res := <-result:
			done(res)
			return
		case <-loading:
		}
		show()
		done(<-result)
	}()
}
