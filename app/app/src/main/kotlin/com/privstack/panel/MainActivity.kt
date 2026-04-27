package com.rknnovpn.panel

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Scaffold
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.lifecycle.lifecycleScope
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import com.rknnovpn.panel.repository.ProfileRepository
import com.rknnovpn.panel.repository.StatusRepository
import com.rknnovpn.panel.ui.navigation.BottomNavBar
import com.rknnovpn.panel.ui.navigation.NavGraph
import com.rknnovpn.panel.ui.navigation.TopLevelRoute
import com.rknnovpn.panel.ui.theme.RKNnoVPNTheme
import dagger.hilt.android.AndroidEntryPoint
import kotlinx.coroutines.launch
import javax.inject.Inject

@AndroidEntryPoint
class MainActivity : ComponentActivity() {
    @Inject lateinit var profileRepository: ProfileRepository
    @Inject lateinit var statusRepository: StatusRepository

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()

        setContent {
            RKNnoVPNTheme {
                val navController = rememberNavController()
                val backStackEntry by navController.currentBackStackEntryAsState()
                val currentRoute = backStackEntry?.destination?.route

                Scaffold(
                    modifier = Modifier.fillMaxSize(),
                    bottomBar = {
                        BottomNavBar(
                            currentRoute = currentRoute,
                            onNavigate = { route ->
                                navController.navigate(route.route) {
                                    popUpTo(TopLevelRoute.Dashboard.route) {
                                        saveState = true
                                    }
                                    launchSingleTop = true
                                    restoreState = true
                                }
                            }
                        )
                    }
                ) { innerPadding ->
                    NavGraph(
                        navController = navController,
                        modifier = Modifier.padding(innerPadding)
                    )
                }
            }
        }
    }

    override fun onResume() {
        super.onResume()
        statusRepository.startPolling()
        lifecycleScope.launch {
            profileRepository.refresh()
        }
    }

    override fun onPause() {
        super.onPause()
        statusRepository.stopPolling()
    }
}
