package io.worktool.open.prototype.policy

import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl

/** The same authorized origin constructs enrollment, session and WebSocket-handshake requests. */
class AuthorizedRoutes(private val policy:EndpointPolicy) {
    companion object {
        const val ENROLL="/api/v1/devices/enroll"
        const val SESSION="/api/v1/device-sessions"
    }
    fun url(endpoint:String,path:String):HttpUrl {
        require(path==ENROLL || path==SESSION) { "ROUTE_NOT_ALLOWED" }
        return policy.authorize(endpoint).toHttpUrl().newBuilder().encodedPath(path).build()
    }
    // OkHttp.newWebSocket uses this exact HTTP-family handshake URL; no separate host input exists.
    fun webSocketHandshake(endpoint:String):HttpUrl=url(endpoint,SESSION)
}
