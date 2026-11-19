package io.worktool.open.prototype.ui

import android.app.Activity
import android.graphics.Color
import android.os.Build
import android.text.InputFilter
import android.text.InputType
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.WindowManager
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

internal object UiViews {
    fun dp(activity: Activity, value: Int): Int = (value * activity.resources.displayMetrics.density).toInt()
    fun secureWindow(activity: Activity) {
        activity.window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        activity.window.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE)
    }
    fun screen(activity: Activity, id: Int): LinearLayout {
        val padding = dp(activity, 18)
        val scroll = ScrollView(activity).apply {
            this.id = id
            isFillViewport = true
            isSaveEnabled = false
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO_EXCLUDE_DESCENDANTS
            setBackgroundColor(Color.rgb(246, 248, 251))
            setPadding(padding, padding, padding, padding)
            setOnApplyWindowInsetsListener { view, insets ->
                if (Build.VERSION.SDK_INT >= 30) {
                    val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime())
                    view.setPadding(padding + bars.left, padding + bars.top, padding + bars.right, padding + bars.bottom)
                } else {
                    @Suppress("DEPRECATION")
                    view.setPadding(padding + insets.systemWindowInsetLeft, padding + insets.systemWindowInsetTop,
                        padding + insets.systemWindowInsetRight, padding + insets.systemWindowInsetBottom)
                }
                insets
            }
        }
        val body = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            isSaveEnabled = false
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO_EXCLUDE_DESCENDANTS
            layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        }
        scroll.addView(body)
        activity.setContentView(scroll)
        scroll.requestApplyInsets()
        return body
    }
    fun text(activity: Activity, body: LinearLayout, value: String, id: Int = View.NO_ID, size: Float = 15f): TextView {
        return TextView(activity).apply {
            this.id = id
            text = value
            textSize = size
            setTextColor(Color.rgb(28, 47, 67))
            isSaveEnabled = false
            layoutParams = LinearLayout.LayoutParams(-1, -2).apply { bottomMargin = dp(activity, 12) }
            body.addView(this)
        }
    }
    fun input(activity: Activity, body: LinearLayout, label: String, id: Int, secret: Boolean = false, maxLength: Int = 2048): EditText {
        val caption = text(activity, body, label, size = 14f).apply { labelFor = id }
        caption.setPadding(0, dp(activity, 6), 0, 0)
        return EditText(activity).apply {
            this.id = id
            isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or
                (if (secret) InputType.TYPE_TEXT_VARIATION_PASSWORD else InputType.TYPE_TEXT_VARIATION_URI)
            imeOptions = EditorInfo.IME_ACTION_NEXT or EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING
            filters = arrayOf(InputFilter.LengthFilter(maxLength))
            isSaveEnabled = false
            setSaveFromParentEnabled(false)
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO_EXCLUDE_DESCENDANTS
            layoutParams = LinearLayout.LayoutParams(-1, -2).apply { bottomMargin = dp(activity, 14) }
            body.addView(this)
        }
    }
    fun button(activity: Activity, body: LinearLayout, label: String, id: Int, action: () -> Unit): Button {
        return Button(activity).apply {
            this.id = id
            text = label
            isAllCaps = false
            isSaveEnabled = false
            minHeight = dp(activity, 48)
            layoutParams = LinearLayout.LayoutParams(-1, -2).apply { bottomMargin = dp(activity, 8) }
            setOnClickListener { action() }
            body.addView(this)
        }
    }
    fun suffix(value: String?): String = value?.takeLast(8)?.replace(Regex("[^A-Za-z0-9_-]"), "_") ?: "未提供"
    fun code(value: String?): String = value?.takeIf { it.matches(Regex("[A-Z0-9_.:-]{1,100}")) } ?: "UNSPECIFIED"
}
