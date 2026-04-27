package com.rknnovpn.panel.ui.common

import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.rknnovpn.panel.R
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

data class AppPackageChoice(
    val packageName: String,
    val label: String,
    val isSystemApp: Boolean,
)

@Composable
fun AppPackagePickerDialog(
    title: String,
    onDismiss: () -> Unit,
    onSelect: (String) -> Unit,
) {
    val context = LocalContext.current
    var query by remember { mutableStateOf("") }
    var apps by remember { mutableStateOf<List<AppPackageChoice>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }

    LaunchedEffect(Unit) {
        apps = withContext(Dispatchers.IO) {
            queryInstalledAppChoices(context.packageManager)
        }
        loading = false
    }

    val normalizedQuery = query.trim().lowercase()
    val filteredApps = remember(apps, normalizedQuery) {
        if (normalizedQuery.isEmpty()) {
            apps
        } else {
            apps.filter {
                it.label.lowercase().contains(normalizedQuery) ||
                    it.packageName.lowercase().contains(normalizedQuery)
            }
        }
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                OutlinedTextField(
                    value = query,
                    onValueChange = { query = it },
                    label = { Text(stringResource(R.string.app_picker_search)) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .heightIn(min = 180.dp, max = 420.dp),
                    contentAlignment = Alignment.Center,
                ) {
                    when {
                        loading -> CircularProgressIndicator()
                        filteredApps.isEmpty() -> Text(stringResource(R.string.app_picker_empty))
                        else -> LazyColumn(modifier = Modifier.fillMaxWidth()) {
                            items(filteredApps, key = { it.packageName }) { app ->
                                ListItem(
                                    headlineContent = { Text(app.label) },
                                    supportingContent = { Text(app.packageName) },
                                    modifier = Modifier.clickable {
                                        onSelect(app.packageName)
                                        onDismiss()
                                    },
                                )
                            }
                        }
                    }
                }
            }
        },
        confirmButton = {},
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(stringResource(R.string.cancel))
            }
        },
    )
}

private fun queryInstalledAppChoices(pm: PackageManager): List<AppPackageChoice> {
    @Suppress("DEPRECATION")
    return pm.getInstalledApplications(PackageManager.GET_META_DATA)
        .map { appInfo ->
            val label = try {
                appInfo.loadLabel(pm).toString()
            } catch (_: Exception) {
                appInfo.packageName
            }
            AppPackageChoice(
                packageName = appInfo.packageName,
                label = label,
                isSystemApp = (appInfo.flags and ApplicationInfo.FLAG_SYSTEM) != 0,
            )
        }
        .sortedWith(compareBy<AppPackageChoice> { it.label.lowercase() }.thenBy { it.packageName })
}
